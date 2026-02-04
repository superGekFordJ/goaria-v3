//go:build !windows

package process

import "os/exec"

func configureCommand(cmd *exec.Cmd) {
	// No-op on Unix
}
