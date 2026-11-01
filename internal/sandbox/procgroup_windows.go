//go:build windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// CREATE_NEW_PROCESS_GROUP is spelled out rather than imported: the syscall
// package exposes it only on some toolchain versions, and the value is stable.
const createNewProcessGroup = 0x00000200

// setProcessGroup isolates the child from Ctrl+C delivered to agenthub and
// gives the deadline a process group to cancel.
//
// Windows has no SIGKILL-by-group equivalent, so a grandchild can outlive a
// timeout. That limitation is why `local` reports isolation "none" and why the
// container and node providers are the supported answer on this platform.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
