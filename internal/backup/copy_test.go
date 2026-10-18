package backup

// The COPY engine is the one that runs when the host has no PostgreSQL client,
// so most of it can only be proved against a real database — that is what
// tests/backup_integration_test.go is for. What is proved here is everything
// that is pure: the framing that carries the dump, and the identifier handling
// that keeps a crafted archive from reaching a SQL string.

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestValidIdentAcceptsOnlyPlainIdentifiers(t *testing.T) {
	ok := []string{"tasks", "session_items", "a", "_private", "t1"}
	bad := []string{
		"", "1task", "Tasks", "task s", "task;s", "tasks\"", "task--",
		"tasks)", "task/*", "таблица", "task\nDROP TABLE tasks",
	}
	for _, name := range ok {
		if !validIdent(name) {
			t.Errorf("validIdent(%q) = false, want true", name)
		}
	}
	for _, name := range bad {
		if validIdent(name) {
			t.Errorf("validIdent(%q) = true, want false", name)
		}
	}
}

func TestQuotingEscapesWhatItWraps(t *testing.T) {
	if got := quoteIdent(`ta"ble`); got != `"ta""ble"` {
		t.Errorf("quoteIdent = %s, want the doubled quote form", got)
	}
	if got := quoteLiteral("o'brien"); got != `'o''brien'` {
		t.Errorf("quoteLiteral = %s, want the doubled quote form", got)
	}
	if got := columnList([]string{"id", "task_id"}); got != `"id", "task_id"` {
		t.Errorf("columnList = %s, want every identifier quoted", got)
	}
	if got := columnList(nil); got != "" {
		t.Errorf("columnList(nil) = %q, want empty", got)
	}
}

// A frame is the whole contract between the writer and the reader: if either
// side drifts, a restore loads the wrong bytes into the wrong table.
func TestWriteFrameRoundTripsBlocks(t *testing.T) {
	var buf bytes.Buffer
	first := "1\tcompleted\n"
	second := "1\ttask-1\n"
	if err := writeFrame(&buf, "tasks", []string{"id", "state"}, strings.NewReader(first), int64(len(first))); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	if err := writeFrame(&buf, "events", []string{"seq", "task_id"}, strings.NewReader(second), int64(len(second))); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	r := bufio.NewReader(&buf)
	table, cols, size, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if table != "tasks" || size != int64(len(first)) {
		t.Fatalf("first block = %q/%d, want tasks/%d", table, size, len(first))
	}
	if strings.Join(cols, ",") != "id,state" {
		t.Fatalf("columns = %v, want [id state]", cols)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(payload) != first {
		t.Fatalf("payload = %q", payload)
	}

	table, cols, size, err = readFrame(r)
	if err != nil {
		t.Fatalf("second readFrame: %v", err)
	}
	if table != "events" || size != int64(len(second)) || strings.Join(cols, ",") != "seq,task_id" {
		t.Fatalf("second block = %q/%d/%v", table, size, cols)
	}
	if _, _, _, err := readFrame(r); err == nil {
		t.Fatal("readFrame after the last block: want the clean end of the payload")
	}
}

// An archive is a file someone could have crafted, so the reader treats every
// name in it as hostile until it matches the identifier rule.
func TestReadFrameRejectsACraftedPayload(t *testing.T) {
	cases := []struct {
		name    string
		table   string
		cols    []string
		wantErr string
	}{
		{"sql in the table name", "tasks; DROP TABLE tasks", []string{"id"}, "bad table name"},
		{"sql in a column", "tasks", []string{"id", "state); DROP TABLE tasks"}, "bad column name"},
		{"no columns", "tasks", nil, "no columns"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := writeFrame(&buf, c.table, c.cols, strings.NewReader("x"), 1); err != nil {
			t.Fatalf("%s: writeFrame: %v", c.name, err)
		}
		_, _, _, err := readFrame(bufio.NewReader(&buf))
		if err == nil {
			t.Fatalf("%s: readFrame accepted the frame", c.name)
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want it to mention %q", c.name, err, c.wantErr)
		}
	}
}

func TestReadFrameRefusesATruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	body := "1\tcompleted\n"
	if err := writeFrame(&buf, "tasks", []string{"id", "state"}, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	raw := buf.Bytes()
	header := len(raw) - len(body)

	// Anything that eats into the framing is refused here: a length that lies
	// about what follows is exactly how a crafted archive gets a loader to read
	// the next block as payload.
	for cut := 1; cut < header; cut++ {
		if _, _, _, err := readFrame(bufio.NewReader(bytes.NewReader(raw[:cut]))); err == nil {
			t.Errorf("readFrame accepted a frame cut to %d bytes", cut)
		}
	}
	// A short payload is not readFrame's business — it hands back the declared
	// length and lets the copy fail, which is what turns a truncated archive
	// into a failed restore rather than a partial one.
	if _, _, size, err := readFrame(bufio.NewReader(bytes.NewReader(raw[:len(raw)-1]))); err != nil {
		t.Fatalf("readFrame on a short payload: %v", err)
	} else if size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", size, len(body))
	}
}

// The reload order is what keeps foreign keys happy: a child loaded before its
// parent is rejected row by row even after a TRUNCATE ... CASCADE.
func TestOrderTablesPutsParentsBeforeChildren(t *testing.T) {
	parentOf := map[string]string{
		"events":         "tasks",
		"task_artifacts": "tasks",
		"session_turns":  "agent_sessions",
	}
	got := orderTables([]string{"events", "tasks", "task_artifacts", "agent_sessions", "session_turns"}, parentOf)
	pos := map[string]int{}
	for i, name := range got {
		pos[name] = i
	}
	if len(got) != 5 {
		t.Fatalf("order = %v, want every table exactly once", got)
	}
	for child, parent := range parentOf {
		if pos[parent] > pos[child] {
			t.Errorf("%s loaded before its parent %s: %v", child, parent, got)
		}
	}
}

func TestOrderTablesIsStableAndIgnoresWhatIsNotInTheArchive(t *testing.T) {
	// A foreign key to a table the archive does not carry, and a table that
	// references itself: neither may change the order or drop a table.
	parentOf := map[string]string{
		"tasks":  "tasks",
		"events": "resource_catalogue",
	}
	got := orderTables([]string{"tasks", "events", "agent_sessions"}, parentOf)
	want := []string{"agent_sessions", "events", "tasks"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// A cycle cannot be satisfied; the loader still has to write every block it
// was given, because losing rows silently is worse than a database error.
func TestOrderTablesKeepsEveryTableInACycle(t *testing.T) {
	got := orderTables([]string{"users", "sessions", "tasks"}, map[string]string{
		"users":    "sessions",
		"sessions": "tasks",
		"tasks":    "users",
	})
	if len(got) != 3 {
		t.Fatalf("order = %v, want all three tables kept", got)
	}
	seen := map[string]bool{}
	for _, name := range got {
		if seen[name] {
			t.Fatalf("order = %v, want each table once", got)
		}
		seen[name] = true
	}
}
