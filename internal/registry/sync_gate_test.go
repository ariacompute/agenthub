package registry

import (
	"context"
	"testing"

	"agenthub/internal/sandbox"
)

// Upstream sync is the second write path an execution policy could arrive
// through, and the answer is that it cannot: entryFromRecord builds only the
// MCP / skill descriptor and drops every other key. That is the safe direction
// (an upstream catalogue must never widen a sandbox), but "we never read it" is
// a property of the parser, so it is pinned here instead of being asserted in a
// comment.
//
// There is deliberately no httptest upstream in this file: the sync client is
// SSRF-guarded and refuses loopback, and — more to the point — even a live
// upstream could not produce a sandbox block to refuse.

func TestUpstreamRecordsCannotCarryAnExecutionPolicy(t *testing.T) {
	for _, kind := range []string{SourceKindMCP, SourceKindSkill} {
		e := entryFromRecord(kind, map[string]any{
			"name":      "upstream-tool",
			"version":   "1.0.0",
			"transport": string(TransportStreamableHTTP),
			// The hostile half: a catalogue that tries to hand itself a
			// writable root and a host directory.
			"sandbox": map[string]any{"readonly_root": false, "work_dir": "/etc"},
		})
		if e == nil {
			t.Fatalf("%s: a named record must still map", kind)
		}
		if e.KindConfig == nil {
			t.Fatalf("%s: a named record must carry its kind descriptor", kind)
		}
		if e.KindConfig.Sandbox != nil {
			t.Fatalf("%s: an upstream record smuggled an execution policy: %+v", kind, e.KindConfig.Sandbox)
		}
	}
}

// Because upstream records cannot name a policy, a sync run neither trips the
// gate nor slips past it: with a gate that refuses every profile, the fixture
// still imports (nothing in the run carries a policy) and the gate is never
// consulted. A future change that starts mapping `sandbox` fails this test,
// which is the point.
func TestSyncNeitherConsultsNorBypassesThePolicyGate(t *testing.T) {
	reg := New()
	var consulted int
	reg.WithSandboxPolicy(func(*SandboxConfig) error {
		consulted++
		return sandbox.Error(sandbox.CodePolicyDenied)
	})
	svc := NewSourceService(reg, nil)
	src, err := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "MCP Registry",
		URL: "https://registry.modelcontextprotocol.test/v0/servers", Fixture: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert source: %v", err)
	}

	res, err := svc.Sync(context.Background(), src.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Status != SyncStatusOK || res.Imported != 3 || res.Skipped != 0 {
		t.Fatalf("sync result = %+v, want three imports and nothing skipped", res)
	}
	if consulted != 0 {
		t.Fatalf("gate consulted %d times for records that name no policy", consulted)
	}
	for _, e := range reg.Snapshot() {
		if e.KindConfig != nil && e.KindConfig.Sandbox != nil {
			t.Fatalf("imported %s carries an execution policy", e.Name)
		}
	}
}
