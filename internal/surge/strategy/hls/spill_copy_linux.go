//go:build linux

package hls

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// rangeCopyChunk caps each copy_file_range call; the kernel may clamp to a
// smaller amount anyway, so the loop expects and tolerates short copies.
const rangeCopyChunk = 1 << 30

// platformRangeCopy uses copy_file_range with explicit offsets (the fd
// positions are untouched: both descriptors stay seeked elsewhere).
func platformRangeCopy(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
	return rangeCopyLoop(dst, src, dstOff, srcOff, length,
		func(rfd int, roff *int64, wfd int, woff *int64, n int) (int, error) {
			return unix.CopyFileRange(rfd, roff, wfd, woff, n, 0)
		})
}

// rawRangeCopy is the syscall seam: read n bytes from rfd at *roff into wfd
// at *woff, updating both offsets by the bytes copied.
type rawRangeCopy func(rfd int, roff *int64, wfd int, woff *int64, n int) (int, error)

// rangeCopyLoop issues cpy until length bytes moved. A short copy loops;
// zero progress with bytes outstanding means the source is short — report it
// instead of spinning. Unsupported errnos degrade to unsupported so the
// caller completes only the unwritten tail.
func rangeCopyLoop(dst, src *os.File, dstOff, srcOff, length int64, cpy rawRangeCopy) (int64, bool, error) {
	var total int64
	for total < length {
		chunk := length - total
		if chunk > rangeCopyChunk {
			chunk = rangeCopyChunk
		}
		sOff := srcOff + total
		dOff := dstOff + total
		n, err := cpy(int(src.Fd()), &sOff, int(dst.Fd()), &dOff, int(chunk))
		if n > 0 {
			total += int64(n)
		}
		if err != nil {
			if isRangeCopyUnsupported(err) {
				return total, true, nil
			}
			return total, false, err
		}
		if n <= 0 {
			return total, false, io.ErrUnexpectedEOF
		}
	}
	return total, false, nil
}

// isRangeCopyUnsupported marks the errno set where the fast path can never
// serve this pair (cross-fs, no kernel support, or an fs that rejects the
// call); the caller finishes the remainder portably.
func isRangeCopyUnsupported(err error) bool {
	return errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EINVAL)
}
