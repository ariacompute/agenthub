package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/netutil"
)

// HTTPNotifier delivers the terminal callback registered for a task by POSTing
// its A2A payload to cfg.URL. It is the real implementation of the MockNotifier's
// TODO(real):
//
//   - SSRF guard: the destination is https only and never resolves to a private,
//     loopback or link-local address (see netutil.SafeClient).
//   - Authenticity: when a push secret is configured the body is signed with
//     HMAC-SHA256 over timestamp + nonce + body, and the receiver gets the
//     timestamp and nonce as headers so it can verify freshness (±5 min) and
//     de-duplicate replays.
//   - Idempotency: a (taskID, state) pair is only delivered once per TTL, so a
//     task that re-enters the same terminal state does not spam the callback.
//
// A failing delivery is logged; it never changes the task's terminal state.
type HTTPNotifier struct {
	client *http.Client
	secret string
	repo   PushRepository

	mu      sync.Mutex
	seen    map[string]time.Time
	ttl     time.Duration
	maxBody int64
}

// NewHTTPNotifier builds a notifier. A non-empty secret enables request signing;
// repo (may be nil) persists the callback so the history survives restarts.
func NewHTTPNotifier(repo PushRepository, secret string) *HTTPNotifier {
	return &HTTPNotifier{
		client:  netutil.SafeClient(10*time.Second, 1<<20, false),
		secret:  secret,
		repo:    repo,
		seen:    make(map[string]time.Time),
		ttl:     time.Hour,
		maxBody: 1 << 20,
	}
}

// Notify validates the destination, signs the payload when a secret is set, and
// POSTs it. It returns nil for a destination that is refused before dial (the
// task state is unaffected either way).
func (n *HTTPNotifier) Notify(ctx context.Context, cfg a2a.PushNotificationConfig, taskID string, state a2a.TaskState) error {
	if err := netutil.ValidateURL(cfg.URL, false); err != nil {
		slog.Warn("hub: push notification refused", "task", taskID, "error", err)
		return nil
	}
	// Idempotent: skip a duplicate (taskID, state) notification within the TTL.
	if !n.markSeen(taskID + "/" + string(state)) {
		return nil
	}

	payload := pushPayload{TaskID: taskID, State: string(state), Config: cfg}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		// The callback's own bearer token authenticates agenthub to the peer.
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	if n.secret != "" {
		ts := time.Now().Unix()
		nonce := newNonce()
		req.Header.Set("X-Agenthub-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Agenthub-Nonce", nonce)
		req.Header.Set("X-Agenthub-Signature", sign(n.secret, ts, nonce, body))
	}

	resp, err := n.client.Do(req)
	if err != nil {
		// A network failure must not fail the task; record the intent and move on.
		slog.Warn("hub: push notification failed", "task", taskID, "state", state, "error", err)
		n.record(cfg, taskID, state)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, n.maxBody))

	// Persist the callback so its history survives a restart, mirroring the mock.
	n.record(cfg, taskID, state)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("hub: push notification delivered", "task", taskID, "state", state, "status", resp.StatusCode)
	} else {
		slog.Warn("hub: push notification rejected by peer", "task", taskID, "state", state, "status", resp.StatusCode)
	}
	return nil
}

func (n *HTTPNotifier) record(cfg a2a.PushNotificationConfig, taskID string, state a2a.TaskState) {
	if n.repo == nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.Background(), pushWriteTimeout)
	defer cancel()
	if err := n.repo.Append(wctx, PushRecord{TaskID: taskID, URL: cfg.URL, State: state, Config: cfg}); err != nil {
		slog.Warn("hub: persist push record failed", "task", taskID, "error", err)
	}
}

// Reset clears the in-memory idempotency cache.
func (n *HTTPNotifier) Reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = make(map[string]time.Time)
}

// markSeen records first sight of key and returns true; a repeat within ttl
// returns false so the caller can skip it.
func (n *HTTPNotifier) markSeen(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if until, ok := n.seen[key]; ok && now.Before(until) {
		return false
	}
	n.seen[key] = now.Add(n.ttl)
	// Best-effort sweep of expired entries.
	if len(n.seen) > 1024 {
		for k, v := range n.seen {
			if !now.Before(v) {
				delete(n.seen, k)
			}
		}
	}
	return true
}

// pushPayload is the body POSTed to the callback.
type pushPayload struct {
	TaskID string                     `json:"taskId"`
	State  string                     `json:"state"`
	Config a2a.PushNotificationConfig `json:"config"`
}

func sign(secret string, ts int64, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte(nonce))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func newNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
