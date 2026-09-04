//go:build windows

package update

import (
	"os/exec"
	"syscall"
)

// applyDetachAttrs configures cmd to run in a detached process group
// without creating a console window on Windows.
func applyDetachAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	const (
		detachedProcess    = 0x00000008
		createNoWindow     = 0x08000000
		createNewProcGroup = 0x00000200
	)
	cmd.SysProcAttr.CreationFlags |= detachedProcess | createNoWindow | createNewProcGroup
	cmd.SysProcAttr.HideWindow = true
}
