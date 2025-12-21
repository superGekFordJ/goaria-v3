package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// TrayState represents the current state of the system tray
type TrayState int

const (
	StateIdle TrayState = iota
	StateActive
	StatePaused
	StateError
)

// GenerateIcon creates a 32x32 PNG icon for the given state
// Brand identity: Lightning bolt merged with download arrow - "Luminous Utility"
// Clean, bold lines optimized for small sizes
func GenerateIcon(state TrayState) []byte {
	size := 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Color schemes for each state - matching brand colors
	var primaryColor, accentColor color.RGBA
	switch state {
	case StateActive:
		primaryColor = color.RGBA{6, 255, 213, 255} // Neon Cyan
		accentColor = color.RGBA{34, 255, 136, 255} // Neon Green
	case StatePaused:
		primaryColor = color.RGBA{251, 191, 36, 255} // Amber
		accentColor = color.RGBA{217, 119, 6, 255}   // Amber darker
	case StateError:
		primaryColor = color.RGBA{239, 68, 68, 255} // Red
		accentColor = color.RGBA{185, 28, 28, 255}  // Red darker
	default: // Idle
		primaryColor = color.RGBA{161, 161, 170, 255} // Zinc-400
		accentColor = color.RGBA{113, 113, 122, 255}  // Zinc-500
	}

	// Draw lightning bolt / download arrow hybrid
	// This is the GoAria brand mark - a stylized "G" merged with a lightning bolt
	drawLightningBolt(img, primaryColor, accentColor, state)

	// Encode to PNG
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// drawLightningBolt draws the brand icon - a lightning bolt representing speed/power
func drawLightningBolt(img *image.RGBA, primary, accent color.RGBA, state TrayState) {
	// Lightning bolt shape - bold and clear at small sizes
	// Designed to look like both a lightning bolt and a download arrow

	// Main bolt body (top to bottom diagonal)
	points := []struct{ x, y int }{
		// Top section (wider)
		{18, 2}, {19, 2}, {20, 2}, {21, 2},
		{17, 3}, {18, 3}, {19, 3}, {20, 3},
		{16, 4}, {17, 4}, {18, 4}, {19, 4},
		{15, 5}, {16, 5}, {17, 5}, {18, 5},
		{14, 6}, {15, 6}, {16, 6}, {17, 6},
		{13, 7}, {14, 7}, {15, 7}, {16, 7},
		// Middle bar (horizontal accent)
		{10, 8}, {11, 8}, {12, 8}, {13, 8}, {14, 8}, {15, 8}, {16, 8}, {17, 8}, {18, 8}, {19, 8}, {20, 8}, {21, 8},
		{10, 9}, {11, 9}, {12, 9}, {13, 9}, {14, 9}, {15, 9}, {16, 9}, {17, 9}, {18, 9}, {19, 9}, {20, 9}, {21, 9},
		// Bottom section (narrower, pointing down)
		{14, 10}, {15, 10}, {16, 10}, {17, 10},
		{15, 11}, {16, 11}, {17, 11}, {18, 11},
		{16, 12}, {17, 12}, {18, 12}, {19, 12},
		{17, 13}, {18, 13}, {19, 13}, {20, 13},
		{18, 14}, {19, 14}, {20, 14}, {21, 14},
		{19, 15}, {20, 15}, {21, 15}, {22, 15},
		// Arrow tip
		{17, 16}, {18, 16}, {19, 16}, {20, 16}, {21, 16}, {22, 16}, {23, 16},
		{18, 17}, {19, 17}, {20, 17}, {21, 17}, {22, 17},
		{19, 18}, {20, 18}, {21, 18},
		{20, 19},
	}

	// Draw main bolt
	for _, p := range points {
		if p.x >= 0 && p.x < 32 && p.y >= 0 && p.y < 32 {
			img.Set(p.x, p.y, primary)
		}
	}

	// Add glow effect for active state
	if state == StateActive {
		// Draw subtle glow around the bolt
		glowColor := color.RGBA{6, 255, 213, 80} // Semi-transparent cyan
		for _, p := range points {
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					nx, ny := p.x+dx, p.y+dy
					if nx >= 0 && nx < 32 && ny >= 0 && ny < 32 {
						existing := img.RGBAAt(nx, ny)
						if existing.A == 0 { // Only draw on transparent pixels
							img.Set(nx, ny, glowColor)
						}
					}
				}
			}
		}
	}

	// Add pause bars overlay for paused state
	if state == StatePaused {
		barColor := color.RGBA{69, 26, 3, 255} // Dark amber
		// Left bar
		drawRect(img, 8, 10, 3, 12, barColor)
		// Right bar
		drawRect(img, 14, 10, 3, 12, barColor)
	}

	// Add X overlay for error state
	if state == StateError {
		xColor := color.RGBA{255, 255, 255, 255} // White X
		// Draw X in bottom right
		for i := 0; i < 6; i++ {
			img.Set(22+i, 22+i, xColor)
			img.Set(22+i, 27-i, xColor)
			img.Set(23+i, 22+i, xColor)
			img.Set(23+i, 27-i, xColor)
		}
	}
}

// drawRect fills a rectangle in the image
func drawRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			if x+dx >= 0 && x+dx < 32 && y+dy >= 0 && y+dy < 32 {
				img.Set(x+dx, y+dy, c)
			}
		}
	}
}

// GetIconForState returns the appropriate icon bytes for a given state
func GetIconForState(state TrayState) []byte {
	return GenerateIcon(state)
}
