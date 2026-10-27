package hub

import (
	"context"
	"testing"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

func TestParkConnectorParksInputRequired(t *testing.T) {
	h, reg, _ := newTestHub(t, &registry.Entry{
		Kind:     registry.KindMCP,
		Name:     "connector",
		Endpoint: "stdio://connector",
		Health:   registry.HealthHealthy,
	})
	var mcp *registry.Entry
	for _, e := range reg.List(string(registry.KindMCP), "") {
		mcp = e
	}
	if mcp == nil {
		t.Fatal("mcp entry not registered")
	}

	task, err := h.ParkConnector(context.Background(), mcp.ID, "invoke")
	if err != nil {
		t.Fatalf("ParkConnector: %v", err)
	}
	if task.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", task.Status.State)
	}
	// The parked task carries the approval prompt the dashboard surfaces as a
	// one-click "approve and run" action.
	pending := h.Store().Pending(task.ID)
	if pending == nil || pending.Prompt != "approved" {
		t.Fatalf("pending = %+v, want prompt=approved", pending)
	}
}

func TestParkConnectorRejectsUnknownResource(t *testing.T) {
	h, _, _ := newTestHub(t)
	if _, err := h.ParkConnector(context.Background(), "missing-resource", "invoke"); err == nil {
		t.Fatal("expected error for an unknown resource")
	} else if rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, err))
	}
}
