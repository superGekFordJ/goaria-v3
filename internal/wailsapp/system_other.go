//go:build !windows

package wailsapp

import "os/exec"

func hideWindow(_ *exec.Cmd) {}
