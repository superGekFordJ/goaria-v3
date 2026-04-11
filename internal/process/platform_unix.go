//go:build !windows

package process

import (
	"os"
	"os/exec"
)

func configureCommand(cmd *exec.Cmd) {
	// No-op on Unix
}

func ensureBundledBinaryPermissions(path string) error {
	return os.Chmod(path, 0o755)
}

func replaceBundledBinary(candidatePath, targetPath string) error {
	return os.Rename(candidatePath, targetPath)
}
