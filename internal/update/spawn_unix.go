//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// applyDetachAttrs configures cmd to run in its own session on POSIX systems
// so it is detached from the parent process.
func applyDetachAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
