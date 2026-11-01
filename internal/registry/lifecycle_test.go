package registry

import (
	"errors"
	"testing"

	"agenthub/internal/httputil"
)

func TestParseStatus(t *testing.T) {
	// Lower-casing is part of normalisation so a stored "Draft" still parses.
	cases := map[string]Status{
		"draft":      StatusDraft,
		"  Pending ": StatusPending,
		"APPROVED":   StatusApproved,
		"rejected":   StatusRejected,
		"deprecated": StatusDeprecated,
	}
	for in, want := range cases {
		got, err := ParseStatus(in)
		if err != nil {
			t.Fatalf("ParseStatus(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseStatus(%q) = %q, want %q", in, got, want)
		}
	}
	// Anything outside the five known states is rejected with the wire code.
	if _, err := ParseStatus("banana"); httputil.AsError(err).Code != CodeInvalidStatusTransition {
		t.Fatalf("ParseStatus(banana) code = %v, want %q", err, CodeInvalidStatusTransition)
	}
}

func TestStatusValidAndDiscoverable(t *testing.T) {
	for _, s := range Statuses {
		if !s.Valid() {
			t.Fatalf("%q should be valid", s)
		}
	}
	if Status("bogus").Valid() {
		t.Fatal("bogus status must not be valid")
	}
	if StatusApproved.Discoverable() != true {
		t.Fatal("approved must be discoverable")
	}
	for _, s := range []Status{StatusDraft, StatusPending, StatusRejected, StatusDeprecated} {
		if s.Discoverable() {
			t.Fatalf("%q must not be discoverable", s)
		}
	}
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		ok       bool
	}{
		{StatusDraft, StatusPending, true},
		{StatusDraft, StatusDeprecated, true},
		{StatusDraft, StatusApproved, false},
		{StatusPending, StatusApproved, true},
		{StatusPending, StatusRejected, true},
		{StatusPending, StatusDeprecated, true},
		{StatusPending, StatusDraft, false},
		{StatusApproved, StatusDeprecated, true},
		{StatusApproved, StatusPending, false},
		{StatusRejected, StatusPending, true},
		{StatusRejected, StatusDeprecated, true},
		{StatusRejected, StatusApproved, false},
		{StatusDeprecated, StatusPending, true},
		{StatusDeprecated, StatusApproved, false},
		{StatusDraft, StatusDraft, true}, // idempotent save is always allowed
		{Status("bogus"), StatusDraft, false},
		{StatusDraft, Status("bogus"), false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Fatalf("CanTransition(%q,%q) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestTransition(t *testing.T) {
	got, err := Transition(StatusPending, StatusApproved)
	if err != nil || got != StatusApproved {
		t.Fatalf("Transition = %q, %v", got, err)
	}
	got, err = Transition(StatusDraft, StatusApproved)
	if err == nil {
		t.Fatal("draft->approved must be rejected")
	}
	if got != StatusDraft {
		t.Fatalf("failed transition must return the original status, got %q", got)
	}
	if !errors.Is(err, &httputil.Error{Code: CodeInvalidStatusTransition}) {
		// Ensure the code is what the console expects.
		if httputil.AsError(err).Code != CodeInvalidStatusTransition {
			t.Fatalf("transition error code = %v", err)
		}
	}
}

func TestDiscoverableFilter(t *testing.T) {
	entries := []*Entry{
		{ID: "1", Status: StatusApproved},
		{ID: "2", Status: StatusPending},
		{ID: "3", Status: StatusDraft},
		{ID: "4", Status: StatusRejected},
		{ID: "5", Status: StatusDeprecated},
		{ID: "6"}, // empty status is treated as approved (legacy records)
		nil,
	}
	out := Discoverable(entries)
	if len(out) != 2 {
		t.Fatalf("Discoverable = %d entries, want 2 (approved + legacy)", len(out))
	}
	for _, e := range out {
		if !e.Status.Valid() {
			continue
		}
		if e.Status != StatusApproved {
			t.Fatalf("Discoverable leaked non-approved entry %q", e.Status)
		}
	}
}
