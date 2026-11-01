package router

import (
	"testing"
	"time"
)

func TestFilterAppliesTheProgramLayer(t *testing.T) {
	// A Sunday morning, so the 22:00-07:00 window is closed and 09:00-17:00 is
	// open. Both windows are expressed the way an operator would type them.
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	none := -1
	cases := []struct {
		name       string
		policy     *Policy
		trigger    Trigger
		wantAdmit  bool
		wantReason string
		wantDefer  bool
	}{
		{
			name:       "no policy admits so a fresh instance is not silent",
			policy:     nil,
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman},
			wantAdmit:  true,
			wantReason: "no_policy",
		},
		{
			name:       "source off drops everything",
			policy:     &Policy{Source: "thread", Mode: PolicyOff, QuietStart: none, QuietEnd: none},
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman, Explicit: true},
			wantReason: "source_off",
		},
		{
			name:       "request only drops an implicit message",
			policy:     &Policy{Source: "thread", Mode: PolicyRequestOnly, QuietStart: none, QuietEnd: none},
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman, Body: "lunch?"},
			wantReason: "request_required",
		},
		{
			name:       "request only admits an explicit one",
			policy:     &Policy{Source: "thread", Mode: PolicyRequestOnly, QuietStart: none, QuietEnd: none},
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman, Explicit: true},
			wantAdmit:  true,
			wantReason: "explicit_request",
		},
		{
			name:       "request only admits a keyword hit",
			policy:     &Policy{Source: "thread", Mode: PolicyRequestOnly, Keywords: []string{"deploy"}, QuietStart: none, QuietEnd: none},
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman, Body: "shall we DEPLOY now"},
			wantAdmit:  true,
			wantReason: "candidate",
		},
		{
			name:       "allowlist silences a person outside it",
			policy:     &Policy{Source: "thread", Mode: PolicyCandidate, Allow: []string{"u-1"}, QuietStart: none, QuietEnd: none},
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman, ActorID: "u-2"},
			wantReason: "actor_not_allowed",
		},
		{
			name:       "allowlist does not silence the scheduler itself",
			policy:     &Policy{Source: "cron", Mode: PolicyCandidate, Allow: []string{"u-1"}, QuietStart: none, QuietEnd: none},
			trigger:    Trigger{Source: "cron", ActorKind: ActorSystem},
			wantAdmit:  true,
			wantReason: "candidate",
		},
		{
			name:       "quiet window defers instead of dropping",
			policy:     &Policy{Source: "thread", Mode: PolicyCandidate, QuietStart: 9 * 60, QuietEnd: 11 * 60},
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman, Explicit: true},
			wantAdmit:  true,
			wantReason: "quiet_window",
			wantDefer:  true,
		},
		{
			name:       "a window that wraps midnight is outside it by day",
			policy:     &Policy{Source: "thread", Mode: PolicyCandidate, QuietStart: 22 * 60, QuietEnd: 7 * 60},
			trigger:    Trigger{Source: "thread", ActorKind: ActorHuman},
			wantAdmit:  true,
			wantReason: "candidate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Filter(tc.policy, &tc.trigger, now)
			if got.Admit != tc.wantAdmit {
				t.Fatalf("admit = %v, want %v (reason %q)", got.Admit, tc.wantAdmit, got.Reason)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if tc.wantDefer {
				if got.Defer.IsZero() || !got.Defer.After(now) {
					t.Fatalf("defer = %v, want a future instant", got.Defer)
				}
				if got.Defer.Hour() != 11 || got.Defer.Minute() != 0 {
					t.Fatalf("defer = %v, want 11:00", got.Defer)
				}
				return
			}
			if !got.Defer.IsZero() {
				t.Fatalf("defer = %v, want none", got.Defer)
			}
		})
	}
}

func TestPolicyValidateRejectsImpossibleValues(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
		wantOK bool
	}{
		{"candidate", Policy{Source: "thread", Mode: PolicyCandidate, QuietStart: -1, QuietEnd: -1}, true},
		{"unknown source", Policy{Source: "carrier-pigeon", Mode: PolicyCandidate, QuietStart: -1, QuietEnd: -1}, false},
		{"unknown mode", Policy{Source: "thread", Mode: "sometimes", QuietStart: -1, QuietEnd: -1}, false},
		{"half a quiet window", Policy{Source: "thread", Mode: PolicyCandidate, QuietStart: 60, QuietEnd: -1}, false},
		{"minute out of range", Policy{Source: "thread", Mode: PolicyCandidate, QuietStart: 60, QuietEnd: 24 * 60}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if (err == nil) != tc.wantOK {
				t.Fatalf("validate = %v, want ok=%v", err, tc.wantOK)
			}
		})
	}
}
