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
		{"GET /healthz", "health probe, also the container healthcheck"},
	}
}
