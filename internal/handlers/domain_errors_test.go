package handlers

// writeDomainErr is the only place a registry / hub / A2A failure becomes an
// HTTP status, and the console leans on both halves of that: the status for its
// retry logic, the code for its localisation. One table keeps the three
// vocabularies from drifting apart.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

func TestWriteDomainErrMapsEveryFailureClass(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"rpc task not found", a2a.NewError(a2a.CodeTaskNotFound, "task_not_found"),
			http.StatusNotFound, "task_not_found"},
		{"rpc resource not found", a2a.NewError(a2a.CodeResourceNotFound, "resource_not_found"),
			http.StatusNotFound, "resource_not_found"},
		{"rpc not cancelable", a2a.NewError(a2a.CodeTaskNotCancelable, "task_not_cancelable"),
			http.StatusBadRequest, "task_not_cancelable"},
		{"rpc not resumable", a2a.NewError(a2a.CodeTaskNotResumable, "task_not_resumable"),
			http.StatusBadRequest, "task_not_resumable"},
		{"rpc state transition", a2a.NewError(a2a.CodeInvalidStateTransition, "invalid_state_transition"),
			http.StatusBadRequest, "invalid_state_transition"},
		{"rpc push unsupported", a2a.NewError(a2a.CodePushNotificationNotSupported, "push_notification_not_supported"),
			http.StatusBadRequest, "push_notification_not_supported"},
		{"rpc unsupported operation", a2a.NewError(a2a.CodeUnsupportedOperation, "unsupported_operation"),
			http.StatusBadRequest, "unsupported_operation"},

		{"registry invalid kind", &httputil.Error{Code: registry.CodeInvalidKind},
			http.StatusBadRequest, string(registry.CodeInvalidKind)},
		{"registry duplicate", &httputil.Error{Code: registry.CodeDuplicate},
			http.StatusBadRequest, string(registry.CodeDuplicate)},
		{"registry sync in flight", &httputil.Error{Code: registry.CodeSyncInFlight},
			http.StatusConflict, string(registry.CodeSyncInFlight)},
		{"registry not found", &httputil.Error{Code: registry.CodeNotFound},
			http.StatusNotFound, string(registry.CodeNotFound)},
		{"hub task not found", &httputil.Error{Code: hub.CodeTaskNotFound},
			http.StatusNotFound, string(hub.CodeTaskNotFound)},
		{"hub not resumable", &httputil.Error{Code: hub.CodeTaskNotResumable},
			http.StatusBadRequest, string(hub.CodeTaskNotResumable)},
		{"hub restoring", &httputil.Error{Code: hub.CodeRestoring},
			http.StatusServiceUnavailable, string(hub.CodeRestoring)},
		{"sandbox policy denied", &httputil.Error{Code: httputil.CodeSandboxPolicyDenied},
			http.StatusForbidden, string(httputil.CodeSandboxPolicyDenied)},
		{"unclassified", errors.New("driver went away"),
			http.StatusInternalServerError, string(httputil.CodeInternal)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeDomainErr(w, c.err)
			if w.Code != c.status {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, c.status, w.Body.String())
			}
			if got := decodeErrorCode(t, w); got != c.code {
				t.Fatalf("code = %q, want %q", got, c.code)
			}
		})
	}
}
