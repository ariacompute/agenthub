package a2a

import (
	"encoding/json"
	"testing"
)

// TestResolveTaskIDAcceptsBothSpellings is the fix for the API inconsistency
// between tasks/pushNotification/set (`taskId`) and .../get (`id`): a client that
// reuses one payload struct must work on both.
func TestResolveTaskIDAcceptsBothSpellings(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		read func(t *testing.T, raw []byte) string
	}{
		{
			name: "query id",
			body: `{"id":"task-1"}`,
			want: "task-1",
			read: func(t *testing.T, raw []byte) string {
				var p TaskQueryParams
				mustUnmarshal(t, raw, &p)
				return p.ResolveTaskID()
			},
		},
		{
			name: "query taskId",
			body: `{"taskId":"task-2"}`,
			want: "task-2",
			read: func(t *testing.T, raw []byte) string {
				var p TaskQueryParams
				mustUnmarshal(t, raw, &p)
				return p.ResolveTaskID()
			},
		},
		{
			name: "push taskId",
			body: `{"taskId":"task-3","pushNotificationConfig":{"url":"https://x.test/hook"}}`,
			want: "task-3",
			read: func(t *testing.T, raw []byte) string {
				var p TaskPushNotificationConfigParams
				mustUnmarshal(t, raw, &p)
				return p.ResolveTaskID()
			},
		},
		{
			name: "push id",
			body: `{"id":"task-4"}`,
			want: "task-4",
			read: func(t *testing.T, raw []byte) string {
				var p TaskPushNotificationConfigParams
				mustUnmarshal(t, raw, &p)
				return p.ResolveTaskID()
			},
		},
		{
			name: "resume taskId",
			body: `{"taskId":"task-5","message":{"role":"user","parts":[{"type":"text","text":"go"}]}}`,
			want: "task-5",
			read: func(t *testing.T, raw []byte) string {
				var p TaskResumeParams
				mustUnmarshal(t, raw, &p)
				return p.ResolveTaskID()
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.read(t, []byte(tc.body)); got != tc.want {
				t.Fatalf("resolved = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveTaskIDPrefersTheCanonicalField makes the conflict rule explicit: a
// caller that sends both gets the specification's field, not the alias.
func TestResolveTaskIDPrefersTheCanonicalField(t *testing.T) {
	if got := ResolveTaskID("canonical", "alias"); got != "canonical" {
		t.Fatalf("ResolveTaskID = %q, want canonical", got)
	}
	var p TaskQueryParams
	mustUnmarshal(t, []byte(`{"id":"a","taskId":"b"}`), &p)
	if got := p.ResolveTaskID(); got != "a" {
		t.Fatalf("tasks/get resolved %q, want the id field", got)
	}
	var push TaskPushNotificationConfigParams
	mustUnmarshal(t, []byte(`{"taskId":"a","id":"b"}`), &push)
	if got := push.ResolveTaskID(); got != "a" {
		t.Fatalf("push resolved %q, want the taskId field", got)
	}
}

// TestResolveTaskIDEmptyWhenNeitherFieldIsSet: the handlers turn an empty id into
// task_not_found, so it must not silently fall back to something else.
func TestResolveTaskIDEmptyWhenNeitherFieldIsSet(t *testing.T) {
	var p TaskQueryParams
	mustUnmarshal(t, []byte(`{}`), &p)
	if got := p.ResolveTaskID(); got != "" {
		t.Fatalf("resolved = %q, want empty", got)
	}
}

func mustUnmarshal(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
}
