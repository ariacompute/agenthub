package registry

import (
	"strings"

	"agenthub/internal/httputil"
)

// Status is the curation state of an entry.
//
// AWS Agent Registry models the same lifecycle (Draft → submitted → approved /
// rejected → deprecated) and only ever exposes approved records to consumers.
// agenthub keeps the identical rule: everything that is consumer facing — the
// MCP export, the Agent Card and A2A resource selection — runs through
// Discoverable, while the control plane keeps showing every state so a curator
// can act on it.
type Status string

const (
	StatusDraft      Status = "draft"
	StatusPending    Status = "pending"
	StatusApproved   Status = "approved"
	StatusRejected   Status = "rejected"
	StatusDeprecated Status = "deprecated"
)

// Statuses lists every status in lifecycle order.
var Statuses = []Status{StatusDraft, StatusPending, StatusApproved, StatusRejected, StatusDeprecated}

// CodeInvalidStatusTransition is returned when a status change is not on the
// transition table. The console localises the code; nothing about the failure
// needs the driver text.
const CodeInvalidStatusTransition = httputil.Code("invalid_status_transition")

// ParseStatus normalises a status string.
func ParseStatus(s string) (Status, error) {
	v := Status(strings.ToLower(strings.TrimSpace(s)))
	for _, c := range Statuses {
		if c == v {
			return c, nil
		}
	}
	return "", &httputil.Error{Code: CodeInvalidStatusTransition}
}

// Valid reports whether the status is one of the five known states.
func (s Status) Valid() bool {
	for _, c := range Statuses {
		if c == s {
			return true
		}
	}
	return false
}

// Discoverable reports whether consumers may find the entry. Only approved
// records are searchable, which mirrors the AWS guarantee that "a consumer
// only sees approved records".
func (s Status) Discoverable() bool { return s == StatusApproved }

// transitions is the whole state machine in one table. A missing key means the
// source state has no outgoing edge at all and every change is rejected.
var transitions = map[Status][]Status{
	StatusDraft:      {StatusPending, StatusDeprecated},
	StatusPending:    {StatusApproved, StatusRejected, StatusDeprecated},
	StatusApproved:   {StatusDeprecated},
	StatusRejected:   {StatusPending, StatusDeprecated},
	StatusDeprecated: {StatusPending},
}

// CanTransition reports whether one status may follow another. Rewriting an
// entry with the status it already has is always allowed: an idempotent save
// must not be mistaken for a lifecycle event.
func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from == to {
		return true
	}
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// Transition validates and returns the next status.
func Transition(from, to Status) (Status, error) {
	if !CanTransition(from, to) {
		return from, &httputil.Error{Code: CodeInvalidStatusTransition}
	}
	return to, nil
}

// Discoverable keeps only the entries a consumer may find.
//
// It is the single filter every consumer facing exit point applies: the MCP
// export, the Agent Card and A2A resource selection all funnel through it, so a
// record that is still being curated can never leak into discovery through a
// path somebody forgot to guard.
func Discoverable(entries []*Entry) []*Entry {
	out := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		if e.EffectiveStatus().Discoverable() {
			out = append(out, e)
		}
	}
	return out
}
