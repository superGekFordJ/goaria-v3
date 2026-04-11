//go:build darwin

package process

import "embed"

//go:embed all:bundled/darwin
var bundledAria2FS embed.FS

func init() {
	currentBundledAria2 = newBundledAria2Source(
		bundledAria2FS,
		"bundled/darwin/aria2c",
		"aria2c",
		"darwin",
		"wails3 task darwin:prepare:aria2 or set ARIA2_BUNDLED_PATH explicitly before rebuilding",
	)
}
