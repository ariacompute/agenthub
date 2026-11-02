package registry

import (
	"strings"

	"agenthub/internal/httputil"
)

// Visibility is the publication state of a catalogue record.
//
// It replaces the rule agenthub started with, where "no grant rows" meant "open
// to anyone holding resources:read". That default was fail-open: a freshly
// registered resource was world readable until somebody remembered to restrict
// it, and an anonymous A2A caller could invoke it in the meantime.
//
// The two states are now explicit:
//
//	restricted  only the platform owner, a principal holding a grant, or the
//	            owner of the record may read or invoke it. This is the default
//	            for every new record.
//	public      deliberately published: readable and invocable by anybody,
//	            including an anonymous A2A peer. A grant is still required to
//	            write, because publishing says nothing about mutability.
type Visibility string

const (
	VisibilityRestricted Visibility = "restricted"
	VisibilityPublic     Visibility = "public"
)

// Visibilities lists every visibility in presentation order.
var Visibilities = []Visibility{VisibilityRestricted, VisibilityPublic}

// CodeInvalidVisibility is returned for a value outside the two states.
const CodeInvalidVisibility = httputil.Code("invalid_visibility")

// ParseVisibility normalises a visibility string.
func ParseVisibility(s string) (Visibility, error) {
	v := Visibility(strings.ToLower(strings.TrimSpace(s)))
	for _, c := range Visibilities {
		if c == v {
			return c, nil
		}
	}
	return "", &httputil.Error{Code: CodeInvalidVisibility}
}

// Valid reports whether v is one of the two known states.
func (v Visibility) Valid() bool {
	for _, c := range Visibilities {
		if c == v {
			return true
		}
	}
	return false
}

// EffectiveVisibility returns the visibility of an entry, reading an empty value
// as restricted. A record written before the column existed therefore stays
// private instead of silently becoming public on upgrade.
func (e *Entry) EffectiveVisibility() Visibility {
	if e == nil || e.Visibility == "" {
		return VisibilityRestricted
	}
	return e.Visibility
}

// IsPublic reports whether anybody may read and invoke the entry without a
// grant. It is the single predicate the visibility layer is allowed to branch
// on, so "public" cannot drift between the store, the handlers and the export.
func (e *Entry) IsPublic() bool { return e.EffectiveVisibility() == VisibilityPublic }
