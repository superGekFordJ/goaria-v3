package types

import (
	"errors"
	"fmt"
)

// AnnotateInsufficientDiskSpace wraps err with ErrInsufficientDiskSpace when
// the underlying cause is a platform disk-full / quota errno. Idempotent when
// err already matches the sentinel. Non-disk errors are returned unchanged.
func AnnotateInsufficientDiskSpace(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInsufficientDiskSpace) {
		return err
	}
	if isDiskFull(err) {
		return fmt.Errorf("%w: %w", err, ErrInsufficientDiskSpace)
	}
	return err
}

// IsInsufficientDiskSpace reports whether err is (or wraps) a disk-full /
// quota failure. True for the sentinel and for raw platform errno values so
// callers remain safe even if a site forgets to annotate.
func IsInsufficientDiskSpace(err error) bool {
	return err != nil && (errors.Is(err, ErrInsufficientDiskSpace) || isDiskFull(err))
}
