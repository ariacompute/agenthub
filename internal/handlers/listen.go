package handlers

// Listener describes one endpoint group served by the single agenthub HTTP
// listener: how to call it and what it is for. The list feeds two places that
// answer "which port serves what": the startup log (an attached
// `docker compose up` or `docker compose logs -f agenthub` shows it) and the
// endpoint table in scripts/compose-ports.sh. Keep both in sync when a route
// moves.
type Listener struct {
	// Endpoint is "<method> <path>" as Register routes it, e.g. "GET /healthz".
	// "/" stands for the console bundle including its client-side routes, and
	// "/api/*" for the whole control plane.
	Endpoint string
	// Use is the one-line purpose shown next to the endpoint.
	Use string
}

// Listeners returns every endpoint group Register wires up, ordered from the
// console down to the operations endpoints. It is derived from the route
// registry itself, so adding or removing a route is a change made here too —
// TestListenersCoverRoutes below is what catches drift.
func Listeners() []Listener {
	return []Listener{
		{"GET /", "Vue 3 console (static bundle and its client-side routes)"},
		{"GET /.well-known/agent.json", "A2A Agent Card, the discovery entry point"},
		{"POST /a2a", "A2A JSON-RPC: message/send, message/stream, tasks/*"},
		{"GET /a2a/stream", "SSE frames for a single task"},
		{"GET /api/auth/config", "login screen: which OAuth providers exist"},
		{"POST /api/auth/login", "email + password login, sets the session cookie"},
		{"GET /api/auth/me", "current identity, permissions and membership"},
		{"GET /api/*", "control plane: resources, tasks, events, push records"},
		{"GET /api/users", "admin: list, create, update and disable users"},
		{"GET /api/roles", "admin: roles, groups and the permission matrix"},
		{"GET /api/grants", "admin: per-resource read/write grants"},
		{"GET /api/backups", "admin: database checkpoints, schedule and restores"},
		{"GET /api/api-keys", "admin: list, create, rotate and revoke API keys"},
		{"GET /api/audit", "audit: the API trail, its summary and the hash chain verifier"},
		{"GET /api/channels", "channels: external messaging surfaces bound to threads"},
		{"POST /api/channels/{id}/webhook", "public inbound webhook for a channel (signed)"},
		{"POST /api/channels/{id}/deliver", "channels: send a message out through a channel's target URL"},
		{"POST /api/channels/{id}/deliveries/{did}/retry", "channels: re-send one outbound receipt that never left"},
		{"GET /api/connectors", "connectors: external applications invoking an mcp resource"},
		{"POST /api/connectors/{id}/actions", "invoke a connector action (policy gated)"},
		{"GET /api/connectors/approvals", "connectors: actions parked by the ask-first policy, waiting for a human"},
		{"POST /api/connectors/approvals/{id}/approve", "connectors: approve a parked action and run it once"},
		{"POST /api/connectors/approvals/{id}/reject", "connectors: reject a parked action, executing nothing"},
		{"POST /api/threads/{id}/audio", "voice message upload, transcribed server side"},
		{"GET /api/computers", "agent computers: browser / shell / screen sandboxes (F11)"},
		{"POST /api/computers", "agent computers: create a sandbox"},
		{"GET /api/computers/{id}", "agent computers: sandbox detail"},
		{"DELETE /api/computers/{id}", "agent computers: terminate the lease and drop the card"},
		{"GET /api/schedules", "schedules: proactive goals (F13)"},
		{"POST /api/schedules", "schedules: create a goal"},
		{"GET /api/schedules/{id}", "schedules: goal detail"},
		{"PUT /api/schedules/{id}", "schedules: edit a goal"},
		{"DELETE /api/schedules/{id}", "schedules: delete a goal"},
		{"POST /api/schedules/{id}/run", "schedules: fire a goal now, through the router"},
		{"GET /api/router/stats", "router: queue depth, tickets in hand, last batch size"},
		{"GET /api/router/triggers", "router: the inbound event stream and what it decided"},
		{"POST /api/router/triggers", "router: inject a trigger by hand"},
		{"GET /api/router/triggers/{id}", "router: one trigger and its verdict"},
		{"GET /api/router/tickets", "router: units of work opened from triggers"},
		{"GET /api/router/tickets/{id}", "router: one ticket with its runs and decisions"},
		{"POST /api/router/tickets/{id}/cancel", "router: stop a ticket and its task"},
		{"POST /api/router/tickets/{id}/handback", "router: return a trigger to the queue"},
		{"DELETE /api/router/threads/{id}/subscription", "router: unsubscribe every ticket from a thread"},
		{"GET /api/router/policies", "router: per-source program layer rules"},
		{"PUT /api/router/policies/{source}", "router: edit one source's rules"},
		{"POST /api/router/passes", "router: run one scheduling beat now"},
		{"GET /api/router/events", "router: SSE trigger / decision / ticket frames"},
		{"GET /api/sandboxes", "sandboxes: live leases and the instance defaults"},
		{"GET /api/sandboxes/stats", "sandboxes: leases, queued jobs, online nodes, last sweep"},
		{"GET /api/sandboxes/events", "sandboxes: SSE lease / exec frames, replayable from since"},
		{"GET /api/sandboxes/{id}", "sandboxes: one lease with its execution audit"},
		{"POST /api/sandboxes/{id}/exec", "sandboxes: run one command in a lease (dry run)"},
		{"POST /api/sandboxes/{id}/renew", "sandboxes: extend a lease"},
		{"DELETE /api/sandboxes/{id}", "sandboxes: terminate a lease and its environment"},
		{"GET /api/sandbox-providers", "sandboxes: provider catalogue grouped by isolation layer"},
		{"POST /api/sandbox-providers/refresh", "sandboxes: re-probe every provider (bypasses the cache)"},
		{"GET /api/sandbox-nodes", "execution nodes: enrolled machines and their liveness"},
		{"POST /api/sandbox-nodes", "execution nodes: enroll or refresh this machine"},
		{"DELETE /api/sandbox-nodes/{id}", "execution nodes: revoke an enrolled machine"},
		{"POST /api/sandbox-nodes/{id}/heartbeat", "execution nodes: liveness beat"},
		{"GET /api/sandbox-nodes/{id}/jobs", "execution nodes: long poll for one job (204 when idle)"},
		{"POST /api/sandbox-nodes/{id}/jobs/{job}", "execution nodes: report a finished job"},
		{"GET /api/knowledge", "knowledge: mock RAG retrieval (F14)"},
		{"GET /api/files", "shared files: the caller's uploads, the size cap and the backend"},
		{"POST /api/files", "shared files: upload one file (multipart)"},
		{"GET /api/files/{id}", "shared files: one file's metadata"},
		{"GET /api/files/{id}/content", "shared files: stream the bytes (Range supported)"},
		{"DELETE /api/files/{id}", "shared files: delete a file and its bytes"},
		{"GET /healthz", "health probe, also the container healthcheck"},
	}
}
