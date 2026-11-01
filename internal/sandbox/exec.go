package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// runSpec is one external process: never a shell string, always an argv.
type runSpec struct {
	bin     string
	args    []string
	dir     string
	env     []string
	stdin   string
	timeout time.Duration
	cap     int
}

// runResult is the raw outcome before it becomes an ExecResult.
type runResult struct {
	stdout    string
	stderr    string
	exit      int
	truncated bool
	duration  time.Duration
}

// runExternal starts a process (never through a shell), waits for it under a
// deadline, and returns truncated output.
//
// Two details matter for safety: the process gets its own process group so a
// timeout kills grandchildren too, and the output is captured through a capped
// writer so a command that prints forever cannot exhaust memory before the
// timeout fires.
func runExternal(ctx context.Context, spec runSpec) (runResult, error) {
	if strings.TrimSpace(spec.bin) == "" {
		return runResult{}, Error(CodePolicyDenied)
	}
	if spec.timeout <= 0 {
		spec.timeout = time.Minute
	}
	if spec.cap <= 0 {
		spec.cap = 64 << 10
	}
	ctx, cancel := context.WithTimeout(ctx, spec.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.bin, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	if spec.stdin != "" {
		cmd.Stdin = strings.NewReader(spec.stdin)
	}
	outBuf := &cappedBuffer{limit: spec.cap}
	errBuf := &cappedBuffer{limit: spec.cap}
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf
	setProcessGroup(cmd)

	started := time.Now()
	err := cmd.Run()
	elapsed := time.Since(started)

	res := runResult{
		stdout:    outBuf.String(),
		stderr:    errBuf.String(),
		truncated: outBuf.truncated || errBuf.truncated,
		duration:  elapsed,
		exit:      0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			// A non-zero exit is a result, not a transport failure: the caller
			// wants the receipt, the console wants the code.
			res.exit = exitErr.ExitCode()
			if res.exit < 0 {
				res.exit = 1
			}
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return res, Error(CodeTimeout)
		case errors.Is(err, exec.ErrNotFound):
			return res, Error(CodeUnsupported)
		default:
			// The binary is missing or not executable: that is a configuration
			// problem about this host, which is exactly what unsupported means.
			return res, Error(CodeUnsupported)
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		// Run returned an exit code because the kill raced the deadline. The
		// honest answer is a timeout, not "exit 137".
		return res, Error(CodeTimeout)
	}
	return res, nil
}

// cappedBuffer keeps at most limit bytes and remembers that it dropped some, so
// the receipt can say "truncated" instead of silently shortening the output.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.truncated = true
		c.buf.Write(p[:remaining])
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// keep is defined so the compile fails loudly if the io import ever becomes
// unnecessary: a capped writer must keep satisfying io.Writer.
var _ io.Writer = (*cappedBuffer)(nil)

// baseEnv is the minimal environment an external command inherits. Nothing else
// from the agenthub process leaks into a sandbox: a secret that is not named in
// the profile cannot be read out of /proc/self/environ by the command that runs
// next.
func baseEnv(allow []string) []string {
	keep := map[string]bool{
		"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true, "TZ": true, "TERM": true,
	}
	for _, name := range allow {
		keep[strings.TrimSpace(name)] = true
	}
	env := make([]string, 0, len(keep))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if keep[name] {
			env = append(env, kv)
		}
	}
	return env
}

// mergeEnv adds the request's explicit variables, which are still filtered
// through the profile's allowlist by the caller.
func mergeEnv(base []string, extra map[string]string, allow []string) []string {
	if len(extra) == 0 {
		return base
	}
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[strings.TrimSpace(name)] = true
	}
	out := append([]string{}, base...)
	for name, value := range extra {
		if !allowed[name] {
			continue
		}
		out = append(out, name+"="+value)
	}
	return out
}

// scrubCommand renders a command line for the audit trail: the binary and the
// argument count, never the arguments themselves. Arguments routinely carry
// tokens, connection strings and file paths that belong to somebody's laptop.
func scrubCommand(command string, args []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if len(args) == 0 {
		return command
	}
	return command + " (+" + itoa(len(args)) + " args)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
