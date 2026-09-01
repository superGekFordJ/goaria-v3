package tray

import _ "embed"

var (
	//go:embed assets/idle.png
	IconIdle []byte

	//go:embed assets/active.png
	IconActive []byte

	//go:embed assets/paused.png
	IconPaused []byte

	//go:embed assets/error.png
	IconError []byte
)
