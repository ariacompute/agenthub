package router

import (
	"strings"
	"time"
)

// FilterVerdict is what the program layer decided about a trigger before any
// model is involved. It is the fast path: cheap, deterministic and, on a real
// event stream, the layer that discards most of the traffic.
type FilterVerdict struct {
	// Admit lets the trigger reach the evaluation (or a subscription).
	Admit bool
	// Defer is non zero when the trigger is admitted but must wait: a quiet
	// window never drops work, it postpones it.
	Defer time.Time
	// Reason is the audit string stored on the trigger so the console can say
	// why something never reached a worker.
	Reason string
}

// Filter applies the program layer to one trigger.
//
// The order matters: "off" is answered first so a disabled source costs nothing,
// the allowlist is applied to people only (an agent or the scheduler itself must
// not be silenced by a human allowlist), and the quiet window is applied last so
// it can only defer a trigger the policies already admitted.
func Filter(p *Policy, t *Trigger, now time.Time) FilterVerdict {
	if p == nil {
		return FilterVerdict{Admit: true, Reason: "no_policy"}
	}
	switch p.Mode {
	case PolicyOff:
		return FilterVerdict{Reason: "source_off"}
	case PolicyRequestOnly:
		if !explicitOrKeyword(p, t) {
			// A request-only source still honours a quiet window for the
			// explicit requests it does admit below; nothing to defer here.
			return FilterVerdict{Reason: "request_required"}
		}
	case PolicyCandidate:
		// Everyone is a candidate; the allowlist and quiet window below narrow
		// it further.
	default:
		return FilterVerdict{Reason: "policy_unset"}
	}

	if t.Human() && len(p.Allow) > 0 && !containsFold(p.Allow, t.ActorID) {
		return FilterVerdict{Reason: "actor_not_allowed"}
	}

	if p.QuietStart >= 0 && p.QuietEnd >= 0 && inQuiet(p.QuietStart, p.QuietEnd, now) {
		return FilterVerdict{Admit: true, Defer: quietEnd(p.QuietStart, p.QuietEnd, now), Reason: "quiet_window"}
	}

	reason := "candidate"
	if t.Explicit {
		reason = "explicit_request"
	}
	return FilterVerdict{Admit: true, Reason: reason}
}

// explicitOrKeyword reports whether the trigger either addresses the router
// directly or contains one of the policy's keywords.
func explicitOrKeyword(p *Policy, t *Trigger) bool {
	if t.Explicit {
		return true
	}
	body := strings.ToLower(t.Body)
	for _, kw := range p.Keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(body, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

// inQuiet reports whether now falls inside the quiet window. A window whose end
// is before its start wraps midnight, which is the common case (22:00 - 07:00).
func inQuiet(start, end int, now time.Time) bool {
	if start == end {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

// quietEnd returns the instant the window closes, starting from now.
func quietEnd(start, end int, now time.Time) time.Time {
	y, m, d := now.Date()
	at := time.Date(y, m, d, end/60, end%60, 0, 0, now.Location())
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}
