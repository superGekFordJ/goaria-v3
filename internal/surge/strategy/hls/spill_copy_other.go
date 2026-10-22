//go:build !linux

package hls

import "os"

// platformRangeCopy reports unsupported on non-Linux platforms so every
// replay takes the bounded portable path.
func platformRangeCopy(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
	return 0, true, nil
}
