// Package handlers is the single route registry of agenthub: the A2A data
// plane, the /api control plane used by the console, /healthz and the static
// console itself.
package handlers

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/config"
	"agenthub/internal/httputil"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
	"agenthub/internal/store"

	"gorm.io/gorm"
)

// Deps is everything the HTTP layer needs.
type Deps struct {
	Cfg *config.Config
	Hub *hub.Hub
	Reg *registry.Registry
	Log *slog.Logger
	// DB is the optional PostgreSQL handle: it only feeds the readiness state
	// reported by /healthz. It stays nil in unit tests.
	DB *gorm.DB
}

// Register builds the router.
func Register(d Deps) http.Handler {
	mux := http.NewServeMux()

	// A2A data plane.
	mux.HandleFunc("GET /.well-known/agent.json", d.handleAgentCard)
	mux.HandleFunc("GET /a2a/agent-card", d.handleAgentCard)
	mux.HandleFunc("POST /a2a", d.handleJSONRPC)
	mux.HandleFunc("GET /a2a/stream", d.requireAdmin(d.handleA2AStream))

	// Control plane (console).
	mux.HandleFunc("GET /api/resources", d.requireAdmin(d.handleListResources))
	mux.HandleFunc("POST /api/resources", d.requireAdmin(d.handleCreateResource))
	mux.HandleFunc("GET /api/resources/{id}", d.requireAdmin(d.handleGetResource))
	mux.HandleFunc("PUT /api/resources/{id}", d.requireAdmin(d.handleUpdateResource))
	mux.HandleFunc("DELETE /api/resources/{id}", d.requireAdmin(d.handleDeleteResource))
	mux.HandleFunc("POST /api/resources/{id}/health", d.requireAdmin(d.handleResourceHealth))

	mux.HandleFunc("GET /api/tasks", d.requireAdmin(d.handleListTasks))
	mux.HandleFunc("GET /api/tasks/{id}", d.requireAdmin(d.handleGetTask))
	mux.HandleFunc("GET /api/tasks/{id}/spans", d.requireAdmin(d.handleTaskSpans))
	mux.HandleFunc("GET /api/tasks/{id}/events", d.requireAdmin(d.handleTaskEventStream))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", d.requireAdmin(d.handleCancelTask))
	mux.HandleFunc("POST /api/tasks/{id}/resume", d.requireAdmin(d.handleResumeTask))

	mux.HandleFunc("GET /api/export/mcp", d.requireAdmin(d.handleExportMCP))
	mux.HandleFunc("GET /api/export/agent-card", d.requireAdmin(d.handleExportAgentCard))

	// Operations.
	mux.HandleFunc("GET /healthz", d.handleHealthz)

	// Console (built by `make web-build`), served last so it never shadows API routes.
	mux.Handle("/", d.staticOrIndex())

	return mux
}

// requireAdmin guards the control plane. A deployment without a token stays
// closed unless it explicitly opts into the local-only open mode.
func (d Deps) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Cfg.ControlPlaneOpen() {
			next(w, r)
			return
		}
		if d.Cfg.AdminToken == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeUnauthorized)
			return
		}
		token := bearerToken(r)
		if subtle.ConstantTimeCompare([]byte(token), []byte(d.Cfg.AdminToken)) != 1 {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeUnauthorized)
			return
		}
		next(w, r)
	}
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	if len(header) > 7 && strings.EqualFold(header[:7], "apikey ") {
		return strings.TrimSpace(header[7:])
	}
	return strings.TrimSpace(header)
}

// baseURL reconstructs the public origin used inside the Agent Card.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// staticOrIndex serves the console bundle, falling back to index.html so the
// Vue router can own client side paths.
func (d Deps) staticOrIndex() http.Handler {
	dir := d.Cfg.StaticDir
	if dir == "" {
		dir = "./backend/static"
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		})
	}
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fs.ServeHTTP(w, r)
			return
		}
		clean := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
		if clean == "" || strings.HasPrefix(clean, "..") {
			fs.ServeHTTP(w, r)
			return
		}
		if _, err := os.Stat(filepath.Join(dir, clean)); err == nil {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}

// healthResponse is the /healthz payload: fields and enums only, never prose.
type healthResponse struct {
	Status          string         `json:"status"`
	Version         string         `json:"version"`
	Commit          string         `json:"commit"`
	BuildNote       string         `json:"buildNote"`
	ProtocolVersion string         `json:"protocolVersion"`
	Env             string         `json:"env"`
	Mode            string         `json:"mode"`
	Resources       map[string]int `json:"resources"`
	Health          map[string]int `json:"health"`
	Tasks           int            `json:"tasks"`
	Methods         []string       `json:"methods"`
	LegacyMethods   bool           `json:"legacyMethods"`
	PushEnabled     bool           `json:"pushEnabled"`
	Auth            string         `json:"auth"`
	Database        string         `json:"database"`
}

// Database state enum reported by /healthz.
const (
	dbStateUp      = "up"
	dbStateDown    = "down"
	dbStateUnknown = "unknown"
)

func (d Deps) handleHealthz(w http.ResponseWriter, r *http.Request) {
	auth := "token"
	if d.Cfg.ControlPlaneOpen() {
		auth = "open"
	}
	status := http.StatusOK
	db := dbStateUnknown
	if d.DB != nil {
		if err := store.Ping(d.DB); err == nil {
			db = dbStateUp
		} else {
			db = dbStateDown
			status = http.StatusServiceUnavailable
			slog.Warn("healthz: database ping failed", "error", err)
		}
	}
	httputil.WriteJSON(w, status, healthResponse{
		Status:          "ok",
		Version:         config.Version,
		Commit:          config.Commit,
		BuildNote:       config.BuildNote(),
		ProtocolVersion: config.ProtocolVersion,
		Env:             d.Cfg.Env,
		Mode:            "mock",
		Resources:       d.Reg.CountsByKind(),
		Health:          d.Reg.HealthCounts(),
		Tasks:           len(d.Hub.Store().List()),
		Methods:         a2a.Methods,
		LegacyMethods:   d.Cfg.LegacyMethods,
		PushEnabled:     d.Cfg.PushEnabled,
		Auth:            auth,
		Database:        db,
	})
}
