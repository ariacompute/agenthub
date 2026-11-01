//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group and makes the context
// deadline kill the whole group. Without this, a shell script that starts three
// children leaves three orphans behind every timeout, which is the difference
// between a sandbox and a leak.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
