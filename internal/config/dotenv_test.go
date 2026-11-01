package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# leading comment\n\nFOO=bar\nexport BAZ=qux\nQUOTED=\"hello world\"\nSINGLE='a b c'\nALREADY=original\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// An existing variable must never be overwritten by the file.
	t.Setenv("ALREADY", "preserved")

	if !applyDotEnv(path) {
		t.Fatal("applyDotEnv returned false on a readable file")
	}

	checks := map[string]string{
		"FOO":     "bar",
		"BAZ":     "qux",
		"QUOTED":  "hello world",
		"SINGLE":  "a b c",
		"ALREADY": "preserved", // unchanged
	}
	for k, want := range checks {
		if got := os.Getenv(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	// A file that does not exist is not an error, just a no-op that returns false.
	if applyDotEnv(filepath.Join(dir, "missing.env")) {
		t.Fatal("missing file should report false")
	}
}

func TestApplyDotEnvSkipsBadLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// A line without "=" and a comment line must be ignored without panicking.
	body := "# comment\nNO_EQUALS_LINE\nKEY=val\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !applyDotEnv(path) {
		t.Fatal("applyDotEnv returned false")
	}
	if os.Getenv("KEY") != "val" {
		t.Fatal("KEY not applied")
	}
	if _, set := os.LookupEnv("NO_EQUALS_LINE"); set {
		t.Fatal("malformed line should be skipped")
	}
}
