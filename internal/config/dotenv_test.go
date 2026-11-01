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

// chdirTemp moves the process into dir for the duration of the test. The restore
// cleanup is registered after t.TempDir()'s own cleanup, so it runs first (LIFO):
// Windows cannot remove a directory that is still the current working directory.
func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}

// clearEnv unsets key for the test and removes it again afterwards, so a value
// exported by LoadDotEnv cannot leak into sibling tests.
func clearEnv(t *testing.T, key string) {
	t.Helper()
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })
}

func TestLoadDotEnvFindsCurrentDir(t *testing.T) {
	const key = "AGENTHUB_DOTENV_CWD"
	clearEnv(t, key)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chdirTemp(t, dir)

	LoadDotEnv()

	if got := os.Getenv(key); got != "here" {
		t.Fatalf("%s = %q, want %q", key, got, "here")
	}
}

func TestLoadDotEnvWalksUpToParent(t *testing.T) {
	const key = "AGENTHUB_DOTENV_PARENT"
	clearEnv(t, key)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(key+"=parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTemp(t, nested)

	LoadDotEnv()

	if got := os.Getenv(key); got != "parent" {
		t.Fatalf("%s = %q, want %q", key, got, "parent")
	}
}

func TestLoadDotEnvNearestFileWins(t *testing.T) {
	const key = "AGENTHUB_DOTENV_NEAREST"
	clearEnv(t, key)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(key+"=far\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".env"), []byte(key+"=near\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chdirTemp(t, sub)

	LoadDotEnv()

	// The first readable file on the way up wins and stops the walk.
	if got := os.Getenv(key); got != "near" {
		t.Fatalf("%s = %q, want %q", key, got, "near")
	}
}

func TestLoadDotEnvStopsAfterDepthLimit(t *testing.T) {
	const key = "AGENTHUB_DOTENV_DEPTH"
	clearEnv(t, key)

	// Seven levels deep with no .env anywhere inside the temp tree: the walk
	// inspects the working directory plus five ancestors and then gives up.
	dir := t.TempDir()
	for i := 0; i < 7; i++ {
		dir = filepath.Join(dir, "d")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTemp(t, dir)

	LoadDotEnv()

	if _, set := os.LookupEnv(key); set {
		t.Fatalf("%s should not be set", key)
	}
}

func TestApplyDotEnvSkipsBadLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// A line without "=" and a comment line must be ignored without panicking.
	// "=orphan" has no key at all and must not blow up os.Setenv with "".
	body := "# comment\nNO_EQUALS_LINE\n=orphan\nKEY=val\n"
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
	if _, set := os.LookupEnv(""); set {
		t.Fatal("a keyless line must be skipped")
	}
}

// At the root of the volume the walk has nowhere left to go; it must stop on the
// parent == dir guard instead of looping forever.
func TestLoadDotEnvStopsAtVolumeRoot(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.VolumeName(cwd) + string(os.PathSeparator)
	if err := os.Chdir(root); err != nil {
		t.Skipf("cannot chdir to the volume root %s: %v", root, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})

	// The only observable contract is "it returns": no panic, no hang.
	LoadDotEnv()
}
