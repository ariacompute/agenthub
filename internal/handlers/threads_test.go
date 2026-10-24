package handlers

import (
	"testing"

	"agenthub/internal/threads"
)

func TestTaskIDsOfDeduplicatesAndSkipsEmpty(t *testing.T) {
	msgs := []*threads.Message{
		{ID: "1", TaskID: "t1"},
		{ID: "2", TaskID: "t1"},
		{ID: "3", TaskID: "t2"},
		{ID: "4", TaskID: ""},
	}
	got := taskIDsOf(msgs)
	if len(got) != 2 || got[0] != "t1" || got[1] != "t2" {
		t.Fatalf("taskIDsOf = %v, want [t1 t2]", got)
	}
}

func TestTaskIDsOfEmpty(t *testing.T) {
	if got := taskIDsOf(nil); len(got) != 0 {
		t.Fatalf("taskIDsOf(nil) = %v, want empty", got)
	}
}
