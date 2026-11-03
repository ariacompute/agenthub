package handlers

// What a handler does when the dep behind it was never wired.
//
// The failing-store cases in store_failures_test.go cover the database going
// away; this file covers the other half of the same contract — a build where the
// administration store, the scheduler or the sandbox control plane is simply
// absent. Both must answer in the protocol rather than in a panic: the console
// renders "this build has no administration", and the A2A peer gets a code it can
// act on. A nil dereference here would take the whole process down on the first
// request.
//
// The handlers are called directly because the middleware always installs a
// principal and a store bundle; an unwired Deps is only reachable without it.

import (
	"encoding/json"
	"net/http"
	"testing"

	"agenthub/internal/authz"
)

// unwiredCase names one surface and the answer it owes when its dependency is
// missing. A 200 case asserts an empty collection under key instead of a code:
// the vocabulary endpoints stay renderable without persistence, which is the
// reason they are not 404s.
type unwiredCase struct {
	name   string
	call   func(Deps, http.ResponseWriter, *http.Request)
	method string
	target string
	body   string
	status int
	code   string
	key    string
	// nonEmpty marks the vocabulary, which is built in: the matrix renders
	// without a store, so the list is expected to have entries.
	nonEmpty bool
}

func TestUnwiredSurfacesAnswerInTheProtocol(t *testing.T) {
	// Administration: the user store and the authorization store are the two
	// things an absent auth subsystem leaves nil.
	admin := []unwiredCase{
		{name: "list users", call: Deps.handleListUsers, target: "/api/users", status: http.StatusNotFound, code: "not_found"},
		{name: "get user", call: Deps.handleGetUser, target: "/api/users/u-1", status: http.StatusNotFound, code: "not_found"},
		{name: "create user", call: Deps.handleCreateUser, method: http.MethodPost, target: "/api/users", body: `{}`, status: http.StatusNotFound, code: "not_found"},
		{name: "update user", call: Deps.handleUpdateUser, method: http.MethodPut, target: "/api/users/u-1", body: `{}`, status: http.StatusNotFound, code: "not_found"},
		{name: "delete user", call: Deps.handleDeleteUser, method: http.MethodDelete, target: "/api/users/u-1", status: http.StatusNotFound, code: "not_found"},
		{name: "revoke user sessions", call: Deps.handleRevokeUserSessions, method: http.MethodDelete, target: "/api/users/u-1/sessions", status: http.StatusNotFound, code: "not_found"},

		// The vocabulary is built in, so the matrix still renders; only saving
		// needs the store.
		{name: "list permissions", call: Deps.handleListPermissions, target: "/api/permissions", status: http.StatusOK, key: "permissions", nonEmpty: true},
		{name: "list roles", call: Deps.handleListRoles, target: "/api/roles", status: http.StatusOK, key: "roles"},
		{name: "create role", call: Deps.handleCreateRole, method: http.MethodPost, target: "/api/roles", body: `{"name":"r"}`, status: http.StatusNotFound, code: "not_found"},
		{name: "update role", call: Deps.handleUpdateRole, method: http.MethodPut, target: "/api/roles/r-1", body: `{}`, status: http.StatusNotFound, code: "not_found"},
		{name: "delete role", call: Deps.handleDeleteRole, method: http.MethodDelete, target: "/api/roles/r-1", status: http.StatusNotFound, code: "not_found"},

		{name: "list groups", call: Deps.handleListGroups, target: "/api/groups", status: http.StatusOK, key: "groups"},
		{name: "create group", call: Deps.handleCreateGroup, method: http.MethodPost, target: "/api/groups", body: `{"name":"g"}`, status: http.StatusNotFound, code: "not_found"},
		{name: "update group", call: Deps.handleUpdateGroup, method: http.MethodPut, target: "/api/groups/g-1", body: `{}`, status: http.StatusNotFound, code: "not_found"},
		{name: "delete group", call: Deps.handleDeleteGroup, method: http.MethodDelete, target: "/api/groups/g-1", status: http.StatusNotFound, code: "not_found"},

		{name: "list grants", call: Deps.handleListGrants, target: "/api/grants", status: http.StatusOK, key: "grants"},
		{name: "create grant", call: Deps.handleCreateGrant, method: http.MethodPost, target: "/api/grants", body: `{}`, status: http.StatusNotFound, code: "not_found"},
		{name: "delete grant", call: Deps.handleDeleteGrant, method: http.MethodDelete, target: "/api/grants/g-1", status: http.StatusNotFound, code: "not_found"},

		// API keys live behind the same store: without it, minting one would be a
		// credential nobody could revoke.
		{name: "list api keys", call: Deps.handleListAPIKeys, target: "/api/keys", status: http.StatusNotFound, code: "not_found"},
		{name: "create api key", call: Deps.handleCreateAPIKey, method: http.MethodPost, target: "/api/keys", body: `{"name":"k"}`, status: http.StatusNotFound, code: "not_found"},
		{name: "get api key", call: Deps.handleGetAPIKey, target: "/api/keys/k-1", status: http.StatusNotFound, code: "not_found"},
		{name: "update api key", call: Deps.handleUpdateAPIKey, method: http.MethodPut, target: "/api/keys/k-1", body: `{}`, status: http.StatusNotFound, code: "not_found"},
		{name: "revoke api key", call: Deps.handleRevokeAPIKey, method: http.MethodDelete, target: "/api/keys/k-1", status: http.StatusNotFound, code: "not_found"},
	}

	// The scheduler and the sandbox control plane are concrete services: absent
	// means the feature is off, which is a 503 the console can name.
	unavailable := []unwiredCase{
		{name: "router stats", call: Deps.handleRouterStats, target: "/api/router/stats"},
		{name: "router triggers", call: Deps.handleListRouterTriggers, target: "/api/router/triggers"},
		{name: "router tickets", call: Deps.handleListRouterTickets, target: "/api/router/tickets"},
		{name: "router policies", call: Deps.handleListRouterPolicies, target: "/api/router/policies"},
		{name: "router kick", call: Deps.handleKickRouterPass, method: http.MethodPost, target: "/api/router/pass"},
		{name: "router events", call: Deps.handleRouterEvents, target: "/api/router/events"},
		{name: "sandboxes", call: Deps.handleListSandboxes, target: "/api/sandboxes"},
		{name: "sandbox stats", call: Deps.handleSandboxStats, target: "/api/sandboxes/stats"},
		{name: "sandbox events", call: Deps.handleSandboxEvents, target: "/api/sandboxes/events"},
	}
	for i := range unavailable {
		unavailable[i].status = http.StatusServiceUnavailable
		unavailable[i].code = "feature_unavailable"
	}

	cases := append(admin, unavailable...)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Only the config is wired: every dep this file is about stays nil.
			d := Deps{Cfg: openConfig("")}

			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			r := sandboxRequest(method, tc.target, tc.body, &authz.Principal{UserID: "owner"}, "u-1")
			w := sandboxRecorder()

			tc.call(d, w, r)

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.status, w.Body.String())
			}
			if tc.code != "" {
				if code := errorCode(t, w); code != tc.code {
					t.Fatalf("code = %q, want %q", code, tc.code)
				}
				return
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
			}
			items, ok := body[tc.key].([]any)
			if !ok {
				t.Fatalf("body has no %q list: %s", tc.key, w.Body.String())
			}
			if tc.nonEmpty {
				if len(items) == 0 {
					t.Fatalf("%s is empty without a store, but the vocabulary is built in", tc.key)
				}
				return
			}
			if len(items) != 0 {
				t.Fatalf("%s = %d entries without a store, want none", tc.key, len(items))
			}
		})
	}
}
