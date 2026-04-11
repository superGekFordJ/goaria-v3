//go:build linux

package process

import "embed"

//go:embed all:bundled/linux
var bundledAria2FS embed.FS

func init() {
	currentBundledAria2 = newBundledAria2Source(
		bundledAria2FS,
		"bundled/linux/aria2c",
		"aria2c",
		"linux",
		"wails3 task linux:prepare:aria2 or set ARIA2_BUNDLED_PATH explicitly before rebuilding",
	)
}
