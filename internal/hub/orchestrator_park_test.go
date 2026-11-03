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

	task, err := h.ParkConnector(context.Background(), mcp.ID, "invoke", map[string]any{"path": "/tmp/a.txt"})
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

	// The queue keys on the marker, and the approver has to be able to read what
	// they are agreeing to: action and arguments travel on the parked message.
	first := task.History[0]
	if marked, _ := first.Metadata["connectorApproval"].(bool); !marked {
		t.Fatalf("metadata = %+v, want the connectorApproval marker", first.Metadata)
	}
	if first.Metadata["resourceId"] != mcp.ID || first.Metadata["connectorAction"] != "invoke" {
		t.Fatalf("metadata = %+v, want resourceId and connectorAction", first.Metadata)
	}
	if args, ok := first.Metadata["args"].(map[string]any); !ok || args["path"] != "/tmp/a.txt" {
		t.Fatalf("args = %+v, want the parked arguments", first.Metadata["args"])
	}

	// The task is visible in the state list the approval queue reads.
	parked := h.ListTasksInState(context.Background(), a2a.TaskStateInputRequired)
	if len(parked) != 1 || parked[0].ID != task.ID {
		t.Fatalf("input-required tasks = %+v, want the parked task", parked)
	}
}

func TestParkConnectorRejectsUnknownResource(t *testing.T) {
	h, _, _ := newTestHub(t)
	if _, err := h.ParkConnector(context.Background(), "missing-resource", "invoke", nil); err == nil {
		t.Fatal("expected error for an unknown resource")
	} else if rpcCode(t, err) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, err))
	}
}
