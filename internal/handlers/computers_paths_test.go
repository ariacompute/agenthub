package handlers

// Creating a computer card is two writes: the row and the environment lease
// behind it. The interesting cases are the second write failing — the card must
// not survive to advertise an environment that was never handed out.

import (
	"context"
	"net/http"
	"testing"

	"agenthub/internal/computers"
)

func computerPathsDeps(t *testing.T, store computers.Store) Deps {
	t.Helper()
	d := testDeps(t, openConfig(""))
	d.Computers = store
	d.Sandboxes = sandboxService(t)
	return d
}

func TestComputerCreateLeasesAnEnvironment(t *testing.T) {
	store := &memComputers{}
	h := Register(computerPathsDeps(t, store))

	w := do(t, h, http.MethodPost, "/api/computers", `{"kind":"browser","agentId":"a1"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", w.Code, w.Body.String())
	}
	var created computers.Computer
	decodeJSON(t, w, &created)
	if created.LeaseID == "" || created.Provider == "" {
		t.Fatalf("created = %+v, want the lease recorded on the card", created)
	}
	if created.Status == "" {
		t.Fatalf("created = %+v, want the lease state copied", created)
	}
}

// TestComputerCreateRollsBackWhenTheLeaseIsRefused: the manager caps leases per
// owner, so the fifth card cannot get an environment. The row must be removed
// again, otherwise the console shows a card that runs nothing.
func TestComputerCreateRollsBackWhenTheLeaseIsRefused(t *testing.T) {
	store := &memComputers{}
	h := Register(computerPathsDeps(t, store))

	for i := 0; i < 4; i++ {
		if w := do(t, h, http.MethodPost, "/api/computers", `{"agentId":"a1"}`, nil); w.Code != http.StatusCreated {
			t.Fatalf("create %d: status = %d, body = %s", i, w.Code, w.Body.String())
		}
	}

	w := do(t, h, http.MethodPost, "/api/computers", `{"agentId":"a1"}`, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("refused lease: status = %d, want 429 (body %s)", w.Code, w.Body.String())
	}
	list, err := store.List(context.Background(), "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("cards after the refused lease = %d, want 4 (the rollback removed the fifth)", len(list))
	}
}

func TestComputerCreateRejectsUndecodableBody(t *testing.T) {
	h := Register(computerPathsDeps(t, &memComputers{}))
	if w := do(t, h, http.MethodPost, "/api/computers", `{"kind":`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// deleteFailingComputers answers reads and writes but refuses the delete, which
// is the only way to reach the handler's last error branch.
type deleteFailingComputers struct{ *memComputers }

func (deleteFailingComputers) Delete(context.Context, string, string) error { return errStoreDown }

func TestComputerDeleteReportsAStoreFailure(t *testing.T) {
	store := &deleteFailingComputers{memComputers: &memComputers{}}
	h := Register(computerPathsDeps(t, store))

	w := do(t, h, http.MethodPost, "/api/computers", `{"agentId":"a1"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", w.Code, w.Body.String())
	}
	var created computers.Computer
	decodeJSON(t, w, &created)

	w = do(t, h, http.MethodDelete, "/api/computers/"+created.ID, "", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete: status = %d, body = %s", w.Code, w.Body.String())
	}

	// The guard inside the handler is only reachable with no store at all: the
	// routes answer 503 before the handler runs when the feature is off.
	d := testDeps(t, openConfig(""))
	req := sandboxRequest(http.MethodGet, "/api/computers/c1", "", nil, "c1")
	rec := sandboxRecorder()
	d.handleGetComputer(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("get without a store: status = %d", rec.Code)
	}
}
