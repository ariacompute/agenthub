// Package handlers is the single route registry of agenthub: the A2A data
// plane, the /api control plane used by the console, /healthz and the static
// console itself.
package handlers

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/backup"
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
	// Auth resolves sessions into identities and Authz turns an identity into
	// permissions. Both stay nil in unit tests, which keeps the open,
	// pre-identity behaviour: every caller is the dev principal.
	Auth  *auth.Service
	Authz *authz.Service
	// Backups is the checkpoint service. Without one the /api/backups/*
	// endpoints report the feature as disabled rather than failing: an
	// instance with no backup volume is a supported configuration.
	Backups *backup.Service
}

// Register builds the router.
func Register(d Deps) http.Handler {
	mux := http.NewServeMux()

	// A2A data plane. Discovery stays public — an agent that has never logged
	// in must be able to find the hub — but it is answered for the caller's
	// identity when there is one, so a granted-only resource is not advertised
	// to the world.
	mux.HandleFunc("GET /.well-known/agent.json", d.identify(d.handleAgentCard))
	mux.HandleFunc("GET /a2a/agent-card", d.identify(d.handleAgentCard))
	mux.HandleFunc("POST /a2a", d.identify(d.handleJSONRPC))
	mux.HandleFunc("GET /a2a/stream", d.requirePermission(authz.PermTasksRead, d.handleA2AStream))

	// Identity. These are the only control plane routes an anonymous caller may
	// reach: without them nobody could obtain a session in the first place.
	mux.HandleFunc("GET /api/auth/config", d.handleAuthConfig)
	mux.HandleFunc("POST /api/auth/register", d.handleRegister)
	mux.HandleFunc("POST /api/auth/login", d.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", d.requireAuth(d.handleLogout))
	mux.HandleFunc("GET /api/auth/me", d.requireAuth(d.handleMe))
	mux.HandleFunc("GET /api/auth/sessions", d.requireAuth(d.handleListSessions))
	mux.HandleFunc("DELETE /api/auth/sessions/{id}", d.requireAuth(d.handleRevokeOwnSession))
	mux.HandleFunc("GET /api/auth/oauth/{provider}", d.handleOAuthStart)
	mux.HandleFunc("GET /api/auth/oauth/{provider}/callback", d.handleOAuthCallback)

	// Control plane (console): one permission per route instead of one shared
	// administrator token.
	mux.HandleFunc("GET /api/resources", d.requirePermission(authz.PermResourcesRead, d.handleListResources))
	mux.HandleFunc("POST /api/resources", d.requirePermission(authz.PermResourcesWrite, d.handleCreateResource))
	mux.HandleFunc("GET /api/resources/{id}", d.requirePermission(authz.PermResourcesRead, d.handleGetResource))
	mux.HandleFunc("PUT /api/resources/{id}", d.requirePermission(authz.PermResourcesWrite, d.handleUpdateResource))
	mux.HandleFunc("DELETE /api/resources/{id}", d.requirePermission(authz.PermResourcesWrite, d.handleDeleteResource))
	mux.HandleFunc("POST /api/resources/{id}/health", d.requirePermission(authz.PermResourcesWrite, d.handleResourceHealth))

	mux.HandleFunc("GET /api/tasks", d.requirePermission(authz.PermTasksRead, d.handleListTasks))
	mux.HandleFunc("GET /api/tasks/{id}", d.requirePermission(authz.PermTasksRead, d.handleGetTask))
	mux.HandleFunc("GET /api/tasks/{id}/spans", d.requirePermission(authz.PermTasksRead, d.handleTaskSpans))
	mux.HandleFunc("GET /api/tasks/{id}/events", d.requirePermission(authz.PermTasksRead, d.handleTaskEventStream))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", d.requirePermission(authz.PermTasksWrite, d.handleCancelTask))
	mux.HandleFunc("POST /api/tasks/{id}/resume", d.requirePermission(authz.PermTasksWrite, d.handleResumeTask))

	mux.HandleFunc("GET /api/export/mcp", d.requirePermission(authz.PermResourcesRead, d.handleExportMCP))
	mux.HandleFunc("GET /api/export/agent-card", d.requirePermission(authz.PermResourcesRead, d.handleExportAgentCard))

	// Administration: users, groups, roles, permissions and resource grants.
	mux.HandleFunc("GET /api/users", d.requirePermission(authz.PermUsersManage, d.handleListUsers))
	mux.HandleFunc("POST /api/users", d.requirePermission(authz.PermUsersManage, d.handleCreateUser))
	mux.HandleFunc("GET /api/users/{id}", d.requirePermission(authz.PermUsersManage, d.handleGetUser))
	mux.HandleFunc("PUT /api/users/{id}", d.requirePermission(authz.PermUsersManage, d.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", d.requirePermission(authz.PermUsersManage, d.handleDeleteUser))
	mux.HandleFunc("DELETE /api/users/{id}/sessions", d.requirePermission(authz.PermSessionsManage, d.handleRevokeUserSessions))

	mux.HandleFunc("GET /api/permissions", d.requirePermission(authz.PermRolesManage, d.handleListPermissions))
	mux.HandleFunc("GET /api/roles", d.requirePermission(authz.PermRolesManage, d.handleListRoles))
	mux.HandleFunc("POST /api/roles", d.requirePermission(authz.PermRolesManage, d.handleCreateRole))
	mux.HandleFunc("PUT /api/roles/{id}", d.requirePermission(authz.PermRolesManage, d.handleUpdateRole))
	mux.HandleFunc("DELETE /api/roles/{id}", d.requirePermission(authz.PermRolesManage, d.handleDeleteRole))

	mux.HandleFunc("GET /api/groups", d.requirePermission(authz.PermRolesManage, d.handleListGroups))
	mux.HandleFunc("POST /api/groups", d.requirePermission(authz.PermRolesManage, d.handleCreateGroup))
	mux.HandleFunc("PUT /api/groups/{id}", d.requirePermission(authz.PermRolesManage, d.handleUpdateGroup))
	mux.HandleFunc("DELETE /api/groups/{id}", d.requirePermission(authz.PermRolesManage, d.handleDeleteGroup))

	mux.HandleFunc("GET /api/grants", d.requirePermission(authz.PermGrantsManage, d.handleListGrants))
	mux.HandleFunc("POST /api/grants", d.requirePermission(authz.PermGrantsManage, d.handleCreateGrant))
	mux.HandleFunc("DELETE /api/grants/{id}", d.requirePermission(authz.PermGrantsManage, d.handleDeleteGrant))

	// Database checkpoints. One permission covers all of them: taking a
	// checkpoint, downloading one, deleting one and restoring the database are
	// the same level of trust in the same operator.
	mux.HandleFunc("GET /api/backups", d.requirePermission(authz.PermBackupsManage, d.handleListBackups))
	mux.HandleFunc("POST /api/backups", d.requirePermission(authz.PermBackupsManage, d.handleCreateBackup))
	mux.HandleFunc("GET /api/backups/schedule", d.requirePermission(authz.PermBackupsManage, d.handleGetBackupSchedule))
	mux.HandleFunc("PUT /api/backups/schedule", d.requirePermission(authz.PermBackupsManage, d.handleSaveBackupSchedule))
	mux.HandleFunc("GET /api/backups/{id}", d.requirePermission(authz.PermBackupsManage, d.handleGetBackup))
	mux.HandleFunc("DELETE /api/backups/{id}", d.requirePermission(authz.PermBackupsManage, d.handleDeleteBackup))
	mux.HandleFunc("GET /api/backups/{id}/download", d.requirePermission(authz.PermBackupsManage, d.handleDownloadBackup))
	mux.HandleFunc("POST /api/backups/{id}/restore", d.requirePermission(authz.PermBackupsManage, d.handleRestoreBackup))

	// Operations.
	mux.HandleFunc("GET /healthz", d.identify(d.handleHealthz))

	// Console (built by `make web-build`), served last so it never shadows API routes.
	mux.Handle("/", d.staticOrIndex())

	return mux
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
	// session | anonymous | disabled: whether this caller is logged in, and
	// whether identities are configured at all.
	auth := "anonymous"
	if d.Auth == nil {
		auth = "disabled"
	} else if p := PrincipalFrom(r); p != nil && !p.Anonymous() {
		auth = "session"
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
