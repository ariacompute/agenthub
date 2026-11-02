package handlers

import (
	"net/http"
	"testing"

	"agenthub/internal/authz"
)

// TestAuditEndpointsRequirePermission pins the gate: the route exists, but a
// principal without audit:read is refused before the handler runs. 403 rather
// than 503 is the point — the guard fires, not the missing recorder.
func TestAuditEndpointsRequirePermission(t *testing.T) {
	d, az := authTestDeps(t, "")
	h := Register(d)

	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	owner := sessionOf(t, h, "owner@example.com", "correct horse")
	w := do(t, h, http.MethodPost, "/api/users",
		`{"email":"audit-member@example.com","password":"correct horse","name":"M"}`, authHeader(owner))
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	member := sessionOf(t, h, "audit-member@example.com", "correct horse")
	az.mu.Lock()
	az.roles[created.User.ID] = []string{"role-viewer"}
	az.rolePerm["role-viewer"] = []string{authz.PermTasksRead}
	az.mu.Unlock()

	for _, target := range []string{"/api/audit", "/api/audit/verify", "/api/audit/abc"} {
		res := do(t, h, http.MethodGet, target, "", authHeader(member))
		if res.Code != http.StatusForbidden {
			t.Fatalf("%s without audit:read = %d, want 403", target, res.Code)
		}
	}
	// The owner holds every permission implicitly, so it reaches the handler and
	// gets the "trail not wired in" answer instead of a refusal.
	if res := do(t, h, http.MethodGet, "/api/audit", "", authHeader(owner)); res.Code != http.StatusServiceUnavailable {
		t.Fatalf("owner on /api/audit = %d, want 503", res.Code)
	}
}

// TestAuditEndpointsReportDisabled: an instance that switched the trail off answers
// 503 rather than 404, which is the difference between "not wired in" and "route
// is gone" that the rest of the API keeps.
func TestAuditEndpointsReportDisabled(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))
	w := do(t, h, http.MethodGet, "/api/audit", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// TestAuditReadIsAVocabularyPermission documents where audit:read comes from: the
// admin role, seeded by migration 027.
func TestAuditReadIsAVocabularyPermission(t *testing.T) {
	if !authz.ValidPermission(authz.PermAuditRead) {
		t.Fatal("audit:read must be part of the permission vocabulary")
	}
}
