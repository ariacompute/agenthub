package a2a

import "testing"

func TestCanonicalMethods(t *testing.T) {
	cases := []struct {
		method string
		legacy bool
		want   string
		ok     bool
	}{
		{"message/send", false, MethodMessageSend, true},
		{"message/stream", false, MethodMessageStream, true},
		{"tasks/get", false, MethodTasksGet, true},
		{"tasks/resume", false, MethodTasksResume, true},
		{"tasks/send", true, MethodMessageSend, true},
		{"tasks/sendSubscribe", true, MethodMessageStream, true},
		{"tasks/send", false, "", false},
		{"nope", true, "", false},
		{"", true, "", false},
	}
	for _, c := range cases {
		got, ok := Canonical(c.method, c.legacy)
		if ok != c.ok || got != c.want {
			t.Fatalf("Canonical(%q, %v) = %q, %v; want %q, %v", c.method, c.legacy, got, ok, c.want, c.ok)
		}
	}
}

func TestTaskStateLifecycle(t *testing.T) {
	if !TaskStateCompleted.IsTerminal() {
		t.Fatal("completed must be terminal")
	}
	if TaskStateInputRequired.IsTerminal() {
		t.Fatal("input-required must not be terminal")
	}
	if !TaskStateWorking.Valid() || TaskState("bogus").Valid() {
		t.Fatal("state validation is wrong")
	}
}

func TestMessageTextAndContext(t *testing.T) {
	m := NewMessage(RoleUser, NewTextPart("hello"), NewTextPart(" world"))
	if got := m.Text(); got != "hello world" {
		t.Fatalf("Text() = %q", got)
	}
	if m.Context() != "" {
		t.Fatalf("Context() = %q, want empty", m.Context())
	}
	m.SessionID = "session-1"
	if got := m.Context(); got != "session-1" {
		t.Fatalf("Context() = %q, want session-1", got)
	}
}

func TestNewTaskStartsSubmitted(t *testing.T) {
	task := NewTask("task-1", NewMessage(RoleUser, NewTextPart("hi")), "demo-agent")
	if task.Status.State != TaskStateSubmitted {
		t.Fatalf("state = %v", task.Status.State)
	}
	if task.Kind != "task" || len(task.History) != 1 {
		t.Fatalf("task = %+v", task)
	}
}

func TestErrorResponseShape(t *testing.T) {
	res := NewErrorResponse(7, NewError(CodeTaskNotFound, MsgTaskNotFound))
	if res.JSONRPC != JSONRPCVersion || res.Error == nil || res.Error.Code != CodeTaskNotFound {
		t.Fatalf("response = %+v", res)
	}
	if res.Error.Error() != MsgTaskNotFound {
		t.Fatalf("error message = %q", res.Error.Error())
	}
}
