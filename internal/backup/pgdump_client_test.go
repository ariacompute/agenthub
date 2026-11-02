package backup

// The shell-out layer, exercised without a database.
//
// pgDumpEngine.Dump and Restore need a live PostgreSQL: they read current_schema
// from it, discover the tables there and fix the sequences afterwards, so their
// real coverage lives in tests/backup_pgdump_test.go behind DATABASE_URL. What is
// proved here is the part that is wrong on every host when it is wrong: how the
// child process is started, and what it is allowed to see.
//
// The helpers at the bottom are not tests. They are this binary re-entered as the
// child process, which is why they behave identically on Linux and Windows —
// unlike a shell script, which one of the two cannot execute.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The markers keep the helpers out of a normal run.
const (
	childMarker = "AGENTHUB_BACKUP_TEST_CHILD"
	sleepMarker = "AGENTHUB_BACKUP_TEST_SLEEP"
)

func TestRunClientHandsThePasswordToTheChildInItsEnvironment(t *testing.T) {
	t.Setenv(childMarker, "1")
	out, err := runClient(context.Background(), os.Args[0],
		[]string{"-test.run=TestRunClientHelperEchoesPassword"}, "s3cr3t-value")
	if err != nil {
		t.Fatalf("run the helper: %v", err)
	}
	// PGPASSWORD is the contract: the password must never be an argv entry.
	if !strings.Contains(string(out), "password=s3cr3t-value") {
		t.Fatalf("the child saw no password in its environment: %q", string(out))
	}
}

func TestRunClientSurfacesAFailedClient(t *testing.T) {
	// A missing binary is the shape a host without the client produces once the
	// probe has been bypassed, and the error has to reach the caller.
	if _, err := runClient(context.Background(), filepath.Join(t.TempDir(), "no-such-pg_dump"), nil, ""); err == nil {
		t.Fatal("a missing client binary must be an error")
	}

	// A client that ran and refused keeps its diagnostic, which is what the
	// sanitised message in the error is built from.
	t.Setenv(childMarker, "1")
	out, err := runClient(context.Background(), os.Args[0],
		[]string{"-test.run=TestRunClientHelperFails"}, "s3cr3t-value")
	if err == nil {
		t.Fatalf("a failing child must be an error, output was %q", string(out))
	}
	if !strings.Contains(string(out), "connection to db failed") {
		t.Fatalf("the child's output was not captured: %q", string(out))
	}
	if got := sanitizeOutput(out, "s3cr3t-value"); strings.Contains(got, "s3cr3t-value") {
		t.Fatalf("the diagnostic kept the password: %q", got)
	}
}

func TestRunClientHonoursTheContextDeadline(t *testing.T) {
	t.Setenv(sleepMarker, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// A child that does not exit on its own: the deadline is what stops it, and
	// that is what keeps a backup beat from blocking a scheduler forever.
	if _, err := runClient(ctx, os.Args[0], []string{"-test.run=TestRunClientHelperSleeps"}, ""); err == nil {
		t.Fatal("a cancelled run must be an error")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("the context was not the thing that stopped the child: %v", ctx.Err())
	}
}

func TestRunClientHelperEchoesPassword(t *testing.T) {
	if os.Getenv(childMarker) != "1" {
		t.Skip("helper process for TestRunClientHandsThePasswordToTheChildInItsEnvironment")
	}
	fmt.Printf("password=%s\n", os.Getenv("PGPASSWORD"))
	os.Exit(0)
}

func TestRunClientHelperFails(t *testing.T) {
	if os.Getenv(childMarker) != "1" {
		t.Skip("helper process for TestRunClientSurfacesAFailedClient")
	}
	fmt.Printf("pg_dump: error: connection to db failed: password %s rejected\n", os.Getenv("PGPASSWORD"))
	os.Exit(1)
}

func TestRunClientHelperSleeps(t *testing.T) {
	if os.Getenv(sleepMarker) != "1" {
		t.Skip("helper process for TestRunClientHonoursTheContextDeadline")
	}
	time.Sleep(5 * time.Second)
	os.Exit(0)
}
