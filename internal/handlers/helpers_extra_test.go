package handlers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/authz"
	"agenthub/internal/backup"
	"agenthub/internal/channels"
	"agenthub/internal/httputil"
)

// The helpers below carry no dependency on a live store, so they are exercised
// directly: they are the pieces every screen ends up trusting (webhook parsing,
// the error code table, optional bodies), and a wrong answer here is wrong for
// every caller at once.

func TestParseWebhookPayloadReadsEveryProviderShape(t *testing.T) {
	cases := []struct {
		name     string
		kind     channels.Kind
		body     string
		wantText string
		wantRef  string
		wantNil  bool
	}{
		{
			name:    "not json",
			kind:    channels.KindSlack,
			body:    "not a payload",
			wantNil: true,
		},
		{
			name:     "telegram",
			kind:     channels.KindTelegram,
			body:     `{"message":{"text":"hello","message_id":42}}`,
			wantText: "hello",
			wantRef:  "tg-42",
		},
		{
			name:     "telegram without a message object",
			kind:     channels.KindTelegram,
			body:     `{"text":"ignored at this level"}`,
			wantText: "",
			wantRef:  "",
		},
		{
			name:     "telegram with a string id",
			kind:     channels.KindTelegram,
			body:     `{"message":{"text":"hi","message_id":"42"}}`,
			wantText: "hi",
			wantRef:  "",
		},
		{
			name:     "slack",
			kind:     channels.KindSlack,
			body:     `{"text":"a message","event_id":"Ev123"}`,
			wantText: "a message",
			wantRef:  "sl-Ev123",
		},
		{
			name:     "generic",
			kind:     channels.Kind("generic"),
			body:     `{"text":"from elsewhere","id":"ext-9"}`,
			wantText: "from elsewhere",
			wantRef:  "ext-9",
		},
		{
			name: "generic without ids",
			kind: channels.Kind("generic"),
			body: `{"body":"no text field"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/channels/x/webhook", nil)
			got, ref := parseWebhookPayload(tc.kind, []byte(tc.body), req)
			if tc.wantNil {
				if got != nil || ref != "" {
					t.Fatalf("payload = %v ref = %q, want both empty", got, ref)
				}
				return
			}
			if got == nil {
				t.Fatal("payload lost")
			}
			// The provider-specific text is normalised into the envelope key the
			// console reads, even when there was none.
			if text, _ := got["text"].(string); text != tc.wantText {
				t.Fatalf("text = %q, want %q", text, tc.wantText)
			}
			want := tc.wantRef
			if ref != want {
				t.Fatalf("external ref = %q, want %q", ref, want)
			}
		})
	}
}

func TestItoaRendersEverySign(t *testing.T) {
	cases := map[int]string{0: "0", 7: "7", 42: "42", -13: "-13"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeOptionalAcceptsAnAbsentBody(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	rec := httptest.NewRecorder()
	if err := decodeOptional(rec, req, &dst, 1024); err != nil {
		t.Fatalf("empty body err = %v, want nil", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/backups", strings.NewReader(`{"name":"cp-1"}`))
	req.ContentLength = int64(len(`{"name":"cp-1"}`))
	rec = httptest.NewRecorder()
	if err := decodeOptional(rec, req, &dst, 1024); err != nil {
		t.Fatalf("body err = %v", err)
	}
	if dst.Name != "cp-1" {
		t.Fatalf("name = %q", dst.Name)
	}

	bad := `{"name":`
	req = httptest.NewRequest(http.MethodPost, "/api/backups", strings.NewReader(bad))
	req.ContentLength = int64(len(bad))
	rec = httptest.NewRecorder()
	if err := decodeOptional(rec, req, &dst, 1024); err == nil {
		t.Fatal("a truncated body must be refused")
	}
}

func TestWriteBackupErrKeepsACodedError(t *testing.T) {
	// A handler that already knows its code must not be re-mapped: the code is
	// what the console switches on.
	rec := httptest.NewRecorder()
	writeBackupErr(rec, httputil.CodeBackupInProgress, &httputil.Error{Code: httputil.CodeBackupNotFound})
	if got := decodeErrorCode(t, rec); got != string(httputil.CodeBackupNotFound) {
		t.Fatalf("code = %q, want the coded one", got)
	}

	rec = httptest.NewRecorder()
	writeBackupErr(rec, httputil.CodeBackupInProgress, backup.ErrBusy)
	if got := decodeErrorCode(t, rec); got != string(httputil.CodeBackupInProgress) {
		t.Fatalf("code = %q, want the caller's busy code", got)
	}
}

func TestBackupCodeMapsEverySentinel(t *testing.T) {
	const busy = httputil.CodeBackupInProgress
	cases := []struct {
		err  error
		want httputil.Code
	}{
		{backup.ErrDisabled, httputil.CodeBackupsDisabled},
		{backup.ErrNotFound, httputil.CodeBackupNotFound},
		{backup.ErrBusy, busy},
		{backup.ErrRestoreConfirm, httputil.CodeRestoreConfirm},
		{backup.ErrSchemaMismatch, httputil.CodeBackupSchemaMismatch},
		{backup.ErrChecksumMismatch, httputil.CodeBackupChecksum},
		{backup.ErrArchive, httputil.CodeBackupInvalidArchive},
		{backup.ErrUnknownTable, httputil.CodeBackupInvalidArchive},
		{backup.ErrEngineUnavailable, httputil.CodeBackupEngineUnavail},
		{backup.ErrTableGuarded, httputil.CodeBackupTableGuarded},
		{backup.ErrEngineFailed, httputil.CodeBackupEngineFailed},
		{backup.ErrRestoreFailed, httputil.CodeRestoreFailed},
		{backup.ErrInvalidMode, httputil.CodeBackupScheduleInvalid},
		{backup.ErrInvalidTime, httputil.CodeBackupScheduleInvalid},
		{backup.ErrInterval, httputil.CodeBackupScheduleInvalid},
		{backup.ErrKeepLast, httputil.CodeBackupScheduleInvalid},
		{backup.ErrMaxAge, httputil.CodeBackupScheduleInvalid},
		{backup.ErrTimezone, httputil.CodeBackupScheduleInvalid},
		{backup.ErrCron, httputil.CodeBackupScheduleInvalid},
		{backup.ErrNoOccurrence, httputil.CodeBackupScheduleInvalid},
		{errors.New("something else"), httputil.CodeBackupFailed},
	}
	for _, tc := range cases {
		if got := backupCode(tc.err, busy); got != tc.want {
			t.Fatalf("backupCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// The same failure is answered differently per endpoint, which is the whole
	// reason busy is a parameter.
	if got := backupCode(backup.ErrBusy, httputil.CodeRestoreBusy); got != httputil.CodeRestoreBusy {
		t.Fatalf("busy code = %q, want the restore one", got)
	}
}

func TestActorOfNamesTheCaller(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/backups/x", nil)
	if got := actorOf(req); got != "anonymous" {
		t.Fatalf("actor = %q, want anonymous", got)
	}
	req = withPrincipal(req, &authz.Principal{UserID: "u-1"})
	if got := actorOf(req); got != "u-1" {
		t.Fatalf("actor = %q, want the principal", got)
	}
}

func TestNextBackupRunWithoutABackendIsNil(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	if got := (Deps{}).nextBackupRun(req); got != nil {
		t.Fatalf("next run = %v, want nil when backups are not wired", got)
	}
}

// decodeErrorCode reads the machine readable code out of an error response.
func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(string(body))
	start := strings.Index(code, `"code"`)
	if start < 0 {
		t.Fatalf("no code in %s", code)
	}
	rest := code[start+len(`"code"`):]
	open := strings.Index(rest, `"`)
	rest = rest[open+1:]
	end := strings.Index(rest, `"`)
	return rest[:end]
}
