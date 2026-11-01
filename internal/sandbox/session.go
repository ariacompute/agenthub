package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// procSession is an attached process speaking line delimited JSON-RPC: the MCP
// stdio transport, and nothing else.
//
// The process is started once and kept alive across RoundTrip calls, because
// initialising an MCP server per request would defeat the point of the transport
// and lose the tool list between steps.
type procSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	mu     sync.Mutex
	closed bool
}

// startSession launches bin+args and wires its stdio.
func startSession(ctx context.Context, spec runSpec) (*procSession, error) {
	if strings.TrimSpace(spec.bin) == "" {
		return nil, Error(CodePolicyDenied)
	}
	cmd := exec.CommandContext(ctx, spec.bin, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, Error(CodeUnsupported)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, Error(CodeUnsupported)
	}
	// stderr is drained, not read: an MCP server's diagnostics must not wedge the
	// session by filling a pipe nobody reads.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, Error(CodeUnsupported)
	}
	if err := cmd.Start(); err != nil {
		return nil, Error(CodeUnsupported)
	}
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()
	return &procSession{cmd: cmd, stdin: stdin, reader: bufio.NewReaderSize(stdout, 64<<10)}, nil
}

// RoundTrip writes one frame and reads one reply line.
func (s *procSession) RoundTrip(ctx context.Context, payload []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, Error(CodeLeaseLost)
	}
	frame := make([]byte, 0, len(payload)+1)
	frame = append(frame, payload...)
	frame = append(frame, '\n')
	if _, err := s.stdin.Write(frame); err != nil {
		return nil, Error(CodeLeaseLost)
	}

	type readResult struct {
		line []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		line, err := s.reader.ReadBytes('\n')
		done <- readResult{line: line, err: err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			if len(bytes.TrimSpace(r.line)) == 0 {
				return nil, Error(CodeUnsupported)
			}
		}
		return bytes.TrimSpace(r.line), nil
	case <-ctx.Done():
		// Killing the process is what unblocks the reader goroutine; leaving it
		// behind would leak one goroutine per timed out call.
		s.kill()
		return nil, Error(CodeTimeout)
	case <-time.After(2 * time.Minute):
		s.kill()
		return nil, Error(CodeTimeout)
	}
}

// Close shuts the session down.
func (s *procSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = s.stdin.Close()
	s.kill()
	return nil
}

func (s *procSession) kill() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Kill()
	_, _ = s.cmd.Process.Wait()
}
