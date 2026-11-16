package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenthub/internal/sandbox"
)

// sandboxQuotas is the smallest sandbox block Validate accepts: the limits below
// break exactly one field at a time so the failing check is unambiguous.
func sandboxQuotas() *Config {
	c := validBaseConfig()
	c.SandboxEnabled = true
	c.SandboxProvider = sandbox.ProviderDocker
	c.SandboxIdleTTL = time.Minute
	c.SandboxMaxTTL = 10 * time.Minute
	c.SandboxMaxPerOwner = 4
	c.SandboxMaxTotal = 16
	c.SandboxNodePollTimeout = 30 * time.Second
	c.SandboxSweepInterval = time.Minute
	c.SandboxCPU, c.SandboxMemoryMB, c.SandboxPidsLimit = 1, 1024, 128
	c.SandboxMaxCPU, c.SandboxMaxMemoryMB, c.SandboxMaxPidsLimit = 4, 4096, 512
	return c
}

// A ceiling of zero is "no ceiling", which no operator means to write and which
// would let a resource ask for the whole host, so it is refused rather than
// filled in: only the wholly zeroed block is a default.
func TestValidateRejectsANonPositiveCeiling(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
	}{
		{"negative cpu", func(c *Config) { c.SandboxMaxCPU = -1 }},
		{"zero memory", func(c *Config) { c.SandboxMaxMemoryMB = 0 }},
		{"zero pids", func(c *Config) { c.SandboxMaxPidsLimit = 0 }},
	} {
		c := sandboxQuotas()
		tc.mut(c)
		if err := c.Validate(); err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
	}
}

// A blank entry in the host-path allow list is not a path: it must be skipped
// instead of being resolved against the server's working directory.
func TestValidateSkipsBlankWorkDirRoots(t *testing.T) {
	c := sandboxQuotas()
	c.SandboxWorkDirRoots = []string{"   ", filepath.VolumeName(os.TempDir()) + string(os.PathSeparator) + "work"}
	if err := c.Validate(); err != nil {
		t.Fatalf("a blank entry must be skipped, got %v", err)
	}
}

// envFloat reads a fractional setting. A typo must not become a zero ceiling,
// so an unparsable value falls back to the default like every other env helper.
func TestEnvFloatFallsBackOnATypo(t *testing.T) {
	const key = "AGENTHUB_TEST_ENV_FLOAT"
	t.Setenv(key, "")
	if got := envFloat(key, 2.5); got != 2.5 {
		t.Fatalf("unset = %g, want the default", got)
	}
	// os.Setenv with an empty value leaves the key set but empty, which is the
	// same "not configured" shape as unset.
	t.Setenv(key, "two-and-a-half")
	if got := envFloat(key, 2.5); got != 2.5 {
		t.Fatalf("unparsable = %g, want the default", got)
	}
	t.Setenv(key, " 1.5 ")
	if got := envFloat(key, 2.5); got != 1.5 {
		t.Fatalf("parsable = %g, want 1.5", got)
	}
}

// A process without a readable working directory cannot start the .env walk;
// it must return quietly instead of panicking at startup.
func TestLoadDotEnvWithoutAWorkingDirectory(t *testing.T) {
	orig := getwd
	t.Cleanup(func() { getwd = orig })
	getwd = func() (string, error) { return "", errors.New("no working directory") }

	LoadDotEnv() // no panic, nothing read
}

// A key the OS refuses (a NUL byte in the name) ends the file, and that still
// counts as "this file was the one": the walk must stop here rather than climb
// to a parent .env and mix two files together.
func TestApplyDotEnvStopsAtAKeyTheOSRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	const good = "AGENTHUB_DOTENV_AFTER_REFUSAL"
	clearEnv(t, good)
	body := "BAD\x00KEY=nope\n" + good + "=applied\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if !applyDotEnv(path) {
		t.Fatal("a refused key must still consume the file, not send the walk upwards")
	}
	if got := os.Getenv(good); got != "" {
		t.Fatalf("%s = %q, want the file to stop at the refused key", good, got)
	}
}
