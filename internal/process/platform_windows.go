//go:build windows

package process

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func ensureBundledBinaryPermissions(path string) error {
	return nil
}

func replaceBundledBinary(candidatePath, targetPath string) error {
	from, err := windows.UTF16PtrFromString(candidatePath)
	if err != nil {
		return err
	}

	to, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}

	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
