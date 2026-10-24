package hls

import (
	"context"
	"errors"
	"os"

	"goaria-v3/internal/surge/types"
)

// hlsMount carries the prepared mount artifacts: the aligned .surge file,
// the prefix seed accepted by the window contract, and the constructed
// window. resumeBaseline records the verified bytes provable before this
// run's init fetch (used to suppress the FirstByte signal on resumes).
type hlsMount struct {
	out            *os.File
	seed           *types.HLSResumeState
	window         *ReorderWindow
	surgePath      string
	spillPath      string
	resumeBaseline int64
}

// mount performs R1 file preparation: open the .surge without truncating
// first, align it to the provable merged prefix, demote every legacy spill
// claim, remove the stale same-ID arena, fetch the init segment when
// needed, then construct the window — with exactly one controlled
// zero-and-refetch fallback when the prepared seed still fails the window
// contract for a non-I/O reason. Failures leave no half-claimed file
// resources.
func (d *HlsDownloader) mount(ctx context.Context, destPath string, seed *types.HLSResumeState) (*hlsMount, error) {
	surgePath := destPath + types.IncompleteSuffix
	if err := validateSpillID(d.ID); err != nil {
		return nil, err
	}
	// Structural descriptor check — bootstrap is the authority for table
	// completeness; a violation here is a contract fault, never a silent
	// "resume as zero".
	if seed == nil || seed.TotalSegments <= 0 || len(seed.Segments) != seed.TotalSegments {
		return nil, newHLSContractError("mount: seed missing a complete media table")
	}
	for i := range seed.Segments {
		if seed.Segments[i].Index != i {
			return nil, newHLSContractError("mount: segment table out of order")
		}
	}

	prepared := seed.Clone()
	if !progressCountersSane(prepared) {
		resetProgress(prepared)
	}

	out, err := os.OpenFile(surgePath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, newStorageError("open", surgePath, err)
	}
	mnt := &hlsMount{out: out, seed: prepared, surgePath: surgePath, spillPath: spillPathFor(surgePath, d.ID)}

	fail := func(err error) (*hlsMount, error) {
		_ = out.Close()
		return nil, err
	}

	fi, err := out.Stat()
	if err != nil {
		return fail(newStorageError("stat", surgePath, err))
	}
	size := fi.Size()
	switch {
	case prepared.MergedBytes <= 0:
		// Fresh or degraded mount: any existing content is a dirty tail.
		if size != 0 {
			if err := out.Truncate(0); err != nil {
				return fail(newStorageError("truncate", surgePath, err))
			}
		}
	case size < prepared.MergedBytes:
		// Claimed prefix is not backed by the file — restart the prefix.
		resetProgress(prepared)
		if err := out.Truncate(0); err != nil {
			return fail(newStorageError("truncate", surgePath, err))
		}
	default:
		// Provable prefix exists; discard everything beyond it.
		if err := out.Truncate(prepared.MergedBytes); err != nil {
			return fail(newStorageError("truncate", surgePath, err))
		}
	}

	// Spill is never re-mounted: demote all >= MergedSegments claims to a
	// prefix-only restart and re-derive verified from merged.
	demoteSpill(prepared)

	// Remove the stale same-ID arena before any new arena can be created;
	// prefix-only seeds still carry this requirement.
	if err := os.Remove(mnt.spillPath); err != nil && !os.IsNotExist(err) {
		return fail(newStorageError("spill remove", mnt.spillPath, err))
	}

	mnt.resumeBaseline = prepared.VerifiedBytes

	// Init must be physically present before the window opens.
	if err := d.prepareInitSegment(ctx, mnt); err != nil {
		return fail(err)
	}

	window, err := NewReorderWindow(out, surgePath, d.ID, prepared)
	if err != nil && errors.Is(err, errDirtySeed) {
		// One controlled fallback: zero the prefix, refetch init, rebuild.
		// I/O faults never take this branch.
		resetProgress(prepared)
		// The sampled baseline is stale after the reset — a fresh start
		// must not look like a resume (it would suppress FirstByte).
		mnt.resumeBaseline = prepared.VerifiedBytes
		if terr := out.Truncate(0); terr != nil {
			return fail(newStorageError("truncate", surgePath, terr))
		}
		if rerr := d.prepareInitSegment(ctx, mnt); rerr != nil {
			return fail(rerr)
		}
		window, err = NewReorderWindow(out, surgePath, d.ID, prepared)
	}
	if err != nil {
		return fail(err)
	}
	mnt.window = window
	return mnt, nil
}

// progressCountersSane reports whether the seed's progress accounting is
// internally consistent enough to judge against the on-disk file. It does
// not validate spill claims — demotion handles those.
func progressCountersSane(s *types.HLSResumeState) bool {
	if s.TotalSegments < 0 || s.MergedSegments < 0 || s.MergedSegments > s.TotalSegments {
		return false
	}
	want := (s.TotalSegments + 7) / 8
	if len(s.CompletedBitmap) != want {
		return false
	}
	if s.TotalSegments&7 != 0 && s.CompletedBitmap[want-1]>>uint(s.TotalSegments&7) != 0 {
		return false
	}
	// The declared prefix must be contiguous — a hole inside the prefix is
	// unprovable, so the whole claim resets.
	for i := 0; i < s.MergedSegments; i++ {
		if s.CompletedBitmap[i>>3]&(1<<uint(i&7)) == 0 {
			return false
		}
	}
	if s.MergedBytes < 0 || s.VerifiedBytes < s.MergedBytes {
		return false
	}
	if s.InitByteOffset < 0 || s.InitByteLength < 0 {
		return false
	}
	if s.InitSegmentURI == "" {
		if s.InitCompleted || s.InitBytes != 0 {
			return false
		}
	} else {
		if s.InitBytes < 0 || s.InitBytes > s.MergedBytes {
			return false
		}
		if s.InitCompleted && s.InitBytes <= 0 {
			return false
		}
		// A merged media prefix must be anchored to a completed init head.
		if s.MergedSegments > 0 && !s.InitCompleted {
			return false
		}
	}
	// A partial init claim is unprovable — the accounting only records the
	// head after a successful full write.
	if !s.InitCompleted && s.InitBytes != 0 {
		return false
	}
	// Merged bytes decompose into init head + media prefix: with no merged
	// media they must equal the init claim, with merged media they must
	// exceed it.
	if s.MergedSegments == 0 && s.MergedBytes != s.InitBytes {
		return false
	}
	if s.MergedSegments > 0 && s.MergedBytes <= s.InitBytes {
		return false
	}
	return true
}

// resetProgress clears every progress claim while keeping the verified
// segment descriptors — the run restarts from byte 0.
func resetProgress(s *types.HLSResumeState) {
	s.CompletedBitmap = make([]byte, (s.TotalSegments+7)/8)
	s.MergedSegments = 0
	s.MergedBytes = 0
	s.VerifiedBytes = 0
	s.SpillPath = ""
	s.SpillFileSize = 0
	s.SpillEntries = nil
	s.InitCompleted = false
	s.InitBytes = 0
}

// clearMediaPrefix drops every merged-media claim — bitmap bits, merged
// count, and spill bookkeeping — while leaving the byte counters for the
// caller to reset (an init rewrite may still re-anchor them).
func clearMediaPrefix(s *types.HLSResumeState) {
	for i := 0; i < s.TotalSegments; i++ {
		s.CompletedBitmap[i>>3] &^= 1 << uint(i&7)
	}
	s.MergedSegments = 0
	s.SpillPath = ""
	s.SpillFileSize = 0
	s.SpillEntries = nil
}

// demoteSpill converts legacy spill bookkeeping into a prefix-only seed:
// every bit at or beyond the merged frontier is cleared, spill fields are
// dropped, and verified collapses to merged.
func demoteSpill(s *types.HLSResumeState) {
	for i := s.MergedSegments; i < s.TotalSegments; i++ {
		s.CompletedBitmap[i>>3] &^= 1 << uint(i&7)
	}
	s.SpillPath = ""
	s.SpillFileSize = 0
	s.SpillEntries = nil
	s.VerifiedBytes = s.MergedBytes
}
