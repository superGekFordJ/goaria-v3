package tray

type TrayState int

const (
	StateIdle TrayState = iota
	StateActive
	StatePaused
	StateError
)

// GetIconForState 返回对应状态的托盘图标 PNG 数据
func GetIconForState(state TrayState) []byte {
	switch state {
	case StateActive:
		return IconActive
	case StatePaused:
		return IconPaused
	case StateError:
		return IconError
	default:
		return IconIdle
	}
}
