//go:build windows

package process

import "embed"

//go:embed all:bundled/windows
var bundledAria2FS embed.FS

func init() {
	currentBundledAria2 = newBundledAria2Source(
		bundledAria2FS,
		"bundled/windows/aria2c.exe",
		"aria2c.exe",
		"windows",
		`powershell -ExecutionPolicy Bypass -File .\setup.ps1`,
	)
}
