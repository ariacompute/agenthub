package handlers

// Visibility decides who may invoke a catalogue record, which makes it the one
// field that must never be guessed at. Two rules hold it down: an update that
// omits the field keeps the record's existing state (the store and the domain
// both read empty as restricted, so silence can never publish), and a value
// outside the two known states is refused instead of being coerced.
//
// Neither rule was exercised before: every create in the suite left the field
// out, so a typo would have reached the store unnoticed.

import (
	"encoding/json"
	"net/http"
	"testing"

	"agenthub/internal/registry"
)

func TestNormalizeVisibility(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "omitted stays empty", in: "", want: "", ok: true},
		{name: "public", in: "public", want: string(registry.VisibilityPublic), ok: true},
		{name: "restricted", in: "restricted", want: string(registry.VisibilityRestricted), ok: true},
		{name: "a typo is refused", in: "pubic", ok: false},
		{name: "an unknown state is refused", in: "internal", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &registry.Entry{Visibility: registry.Visibility(tc.in)}
			err := normalizeVisibility(entry)
			if tc.ok && err != nil {
				t.Fatalf("normalize(%q) = %v, want nil", tc.in, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("normalize(%q) accepted an unknown state", tc.in)
				}
				return
			}
			if string(entry.Visibility) != tc.want {
				t.Fatalf("normalize(%q) = %q, want %q", tc.in, entry.Visibility, tc.want)
			}
		})
	}
}

func TestUpdatingAResourceRefusesAnUnknownVisibility(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))

	created := do(t, h, http.MethodPost, "/api/resources", `{"kind":"model","name":"vis"}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", created.Code, created.Body.String())
	}
	var entry registry.Entry
	if err := json.Unmarshal(created.Body.Bytes(), &entry); err != nil {
		t.Fatalf("create body: %v", err)
	}

	// A typo reaches the store as nothing at all: the whole point is that the
	// record keeps the visibility it had.
	bad := do(t, h, http.MethodPut, "/api/resources/"+entry.ID,
		`{"kind":"model","name":"vis","visibility":"pubic"}`, nil)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown visibility: status = %d, want 400 (body %s)", bad.Code, bad.Body.String())
	}
	if code := errCode(t, bad); code != string(registry.CodeInvalidVisibility) {
		t.Fatalf("code = %q, want %q", code, registry.CodeInvalidVisibility)
	}

	// Publishing has to be asked for: an update that omits the field leaves the
	// record restricted.
	omitted := do(t, h, http.MethodPut, "/api/resources/"+entry.ID, `{"kind":"model","name":"vis"}`, nil)
	if omitted.Code != http.StatusOK {
		t.Fatalf("update without visibility: status = %d, body = %s", omitted.Code, omitted.Body.String())
	}
	var stored registry.Entry
	if err := json.Unmarshal(omitted.Body.Bytes(), &stored); err != nil {
		t.Fatalf("update body: %v", err)
	}
	if stored.EffectiveVisibility() != registry.VisibilityRestricted {
		t.Fatalf("visibility = %q after an update that named none, want restricted", stored.Visibility)
	}

	// And asking for it works.
	published := do(t, h, http.MethodPut, "/api/resources/"+entry.ID,
		`{"kind":"model","name":"vis","visibility":"public"}`, nil)
	if published.Code != http.StatusOK {
		t.Fatalf("publish: status = %d, body = %s", published.Code, published.Body.String())
	}
	var public registry.Entry
	if err := json.Unmarshal(published.Body.Bytes(), &public); err != nil {
		t.Fatalf("publish body: %v", err)
	}
	if public.EffectiveVisibility() != registry.VisibilityPublic {
		t.Fatalf("visibility = %q after publishing, want public", public.Visibility)
	}
}

// A refusal outside the audit middleware — no tracer in the context — must not
// panic: the trajectory is a nice-to-have, the refusal is the answer.
func TestNoteRefusalWithoutAnAuditTracer(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/a2a", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	noteRefusal(req, "forbidden")
}
