package hls

import (
	"context"
	"crypto/aes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

// HLS-local progress batching — the shared types.WorkerBatch* constants are
// byte-range engine territory and stay untouched.
const (
	hlsWorkerBatchSize     = 64 * 1024
	hlsWorkerBatchInterval = 50 * time.Millisecond
)

// hlsSegBufPool recycles growable single-segment buffers; buffers that grew
// past types.MaxPoolBufferCap are dropped rather than pinned in the pool.
var hlsSegBufPool = sync.Pool{New: func() any {
	b := make([]byte, 0, hlsWorkerBatchSize)
	return &b
}}

func hlsBufGet() []byte {
	return (*hlsSegBufPool.Get().(*[]byte))[:0]
}

func hlsBufPut(b []byte) {
	if cap(b) > types.MaxPoolBufferCap {
		return
	}
	hlsSegBufPool.Put(&b)
}

// growSegBuf doubles capacity until the tail fits need more bytes; growth
// follows actual reads, never an untrusted Content-Length.
func growSegBuf(buf []byte, need int) []byte {
	newCap := max(cap(buf)*2, len(buf)+need)
	newCap = max(newCap, hlsWorkerBatchSize)
	nb := make([]byte, len(buf), newCap)
	copy(nb, buf)
	return nb
}

// hlsSegmentByteRange validates the descriptor: both zero means a normal
// full-body GET; a ranged segment needs ByteLength>0, ByteOffset>=0 and a
// non-overflowing end — anything else is a contract violation before the
// request leaves.
func hlsSegmentByteRange(seg *types.HLSSegmentState) (*hlsByteRange, error) {
	if seg.ByteLength == 0 && seg.ByteOffset == 0 {
		return nil, nil
	}
	if seg.ByteLength <= 0 || seg.ByteOffset < 0 ||
		seg.ByteLength > math.MaxInt64-seg.ByteOffset {
		return nil, newHLSContractError("segment %d invalid byte range", seg.Index)
	}
	return &hlsByteRange{offset: seg.ByteOffset, length: seg.ByteLength}, nil
}

// validateSegmentResponse enforces the response contract past the status
// check: identity encoding only, no multipart, and for a 206 a single
// Content-Range exactly matching the requested span.
func validateSegmentResponse(resp *http.Response, br *hlsByteRange, segIndex int) error {
	if enc := resp.Header.Get("Content-Encoding"); enc != "" &&
		!strings.EqualFold(strings.TrimSpace(enc), "identity") {
		return fmt.Errorf("hls segment %d: content-encoding: %w",
			segIndex, types.ErrHLSInvalidSegmentResponse)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(
		strings.ToLower(strings.TrimSpace(ct)), "multipart/") {
		return fmt.Errorf("hls segment %d: multipart response: %w",
			segIndex, types.ErrHLSInvalidSegmentResponse)
	}
	if br == nil {
		return nil
	}
	unit, start, end, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || unit != "bytes" || start != br.offset || end != br.offset+br.length-1 {
		return fmt.Errorf("hls segment %d: content-range mismatch: %w",
			segIndex, types.ErrHLSInvalidSegmentResponse)
	}
	if total >= 0 && total <= end {
		return fmt.Errorf("hls segment %d: content-range total: %w",
			segIndex, types.ErrHLSInvalidSegmentResponse)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != br.length {
		return fmt.Errorf("hls segment %d: content-length mismatch: %w",
			segIndex, types.ErrHLSInvalidSegmentResponse)
	}
	return nil
}

// parseContentRange parses "bytes <start>-<end>/<total|*>"; total is -1 for
// the star form.
func parseContentRange(v string) (unit string, start, end, total int64, ok bool) {
	v = strings.TrimSpace(v)
	sp := strings.IndexByte(v, ' ')
	if sp <= 0 {
		return "", 0, 0, 0, false
	}
	unit = strings.ToLower(v[:sp])
	rest := v[sp+1:]
	rangePart, totalPart, found := strings.Cut(rest, "/")
	if !found {
		return "", 0, 0, 0, false
	}
	startPart, endPart, found := strings.Cut(rangePart, "-")
	if !found {
		return "", 0, 0, 0, false
	}
	var err error
	if start, err = strconv.ParseInt(startPart, 10, 64); err != nil {
		return "", 0, 0, 0, false
	}
	if end, err = strconv.ParseInt(endPart, 10, 64); err != nil {
		return "", 0, 0, 0, false
	}
	total = -1
	if totalPart != "*" {
		if total, err = strconv.ParseInt(totalPart, 10, 64); err != nil {
			return "", 0, 0, 0, false
		}
	}
	return unit, start, end, total, true
}

// execMediaAttempt is the production per-attempt body: consume any pending
// manifest-refresh mark first (the attempt's own segCtx bounds that wait),
// one locked URI/key-table snapshot, host-penalty wait, one gated GET,
// strict status/headers validation, streaming body reads into a pooled
// growable buffer with the 64KiB/50ms dual-counter ledger, private key
// table + in-place decrypt, pad correction, then the commit gate and
// Accepted settlement. The attempt never re-reads task.seg or e.seed.Keys
// after the snapshot — a mid-attempt refresh adoption can never mix
// generations inside this attempt.
func (e *segmentEngine) execMediaAttempt(sess *hlsWorkerSession, at *hlsAttempt) error {
	e.refreshManifestOnAuthExpired(sess, at)
	seg, keys := e.attemptURISnapshot(at.task)
	br, err := hlsSegmentByteRange(&seg)
	if err != nil {
		return err
	}
	if err := e.waitHostPenalty(sess, at, seg.URI); err != nil {
		return err
	}
	sess.setPhase(hlsPhaseNetwork)
	req, err := newHLSSubRequest(at.segCtx, seg.URI, e.taskHeaders, e.credBase, e.runCfg, br)
	if err != nil {
		return err
	}
	at.noteStatus(0)
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("hls segment %d: %w", seg.Index, &hlsTransportError{err: err})
	}
	at.noteActivity()
	at.noteStatus(resp.StatusCode)
	defer resp.Body.Close()

	expected := http.StatusOK
	if br != nil {
		expected = http.StatusPartialContent
	}
	if resp.StatusCode != expected {
		if resp.StatusCode >= 400 {
			return &hlsStatusError{
				resource:   "segment",
				index:      seg.Index,
				status:     resp.StatusCode,
				retryAfter: resp.Header.Get("Retry-After"),
				host:       transport.MirrorHost(seg.URI),
			}
		}
		return fmt.Errorf("hls segment %d: unexpected status %d: %w",
			seg.Index, resp.StatusCode, types.ErrHLSInvalidSegmentResponse)
	}
	if err := validateSegmentResponse(resp, br, seg.Index); err != nil {
		return err
	}
	e.hostLimiter.RecordSuccess(transport.MirrorHost(seg.URI))

	buf := hlsBufGet()
	defer func() { hlsBufPut(buf) }()
	// Every exit before acceptance rolls this attempt's credit back exactly
	// once; unpublished pending bytes are simply dropped.
	defer func() {
		if !at.accepted && at.credited != 0 {
			e.prog.Bytes.Downloaded.Add(-at.credited)
			e.prog.Bytes.VerifiedProgress.Add(-at.credited)
			at.credited = 0
		}
	}()

	var pending int64
	lastFlush := e.now()
	flush := func() {
		if pending <= 0 {
			return
		}
		e.prog.Bytes.Downloaded.Add(pending)
		e.prog.Bytes.VerifiedProgress.Add(pending)
		at.credited += pending
		pending = 0
		lastFlush = e.now()
	}

	for {
		// Publish previously token'd bytes before a potentially blocking
		// read — also the bounded-wait flush before the next WaitN.
		flush()
		want := hlsWorkerBatchSize
		if br != nil {
			remaining := br.length + 1 - int64(len(buf))
			if remaining <= 0 {
				break // the +1 probe already proved oversize
			}
			if remaining < int64(want) {
				want = int(remaining)
			}
		}
		if cap(buf)-len(buf) < want {
			buf = growSegBuf(buf, want)
		}
		n, rerr := resp.Body.Read(buf[len(buf) : len(buf)+want])
		if n > 0 {
			buf = buf[:len(buf)+n]
			at.noteActivity()
			sess.sessionBytes.Add(int64(n))
			sess.windowBytes.Add(int64(n))
			if terr := at.throttle(at.segCtx, int64(n)); terr != nil {
				return terr
			}
			pending += int64(n)
			if pending >= hlsWorkerBatchSize || e.now().Sub(lastFlush) >= hlsWorkerBatchInterval {
				flush()
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return fmt.Errorf("hls segment %d: body read: %w", seg.Index, rerr)
		}
		if n == 0 {
			return fmt.Errorf("hls segment %d: body read: %w", seg.Index, io.ErrNoProgress)
		}
	}
	flush()

	got := int64(len(buf))
	if br != nil {
		if got > br.length {
			return fmt.Errorf("hls segment %d: body exceeds range length: %w",
				seg.Index, types.ErrHLSInvalidSegmentResponse)
		}
		if got < br.length {
			return fmt.Errorf("hls segment %d: truncated body %d/%d: %w",
				seg.Index, got, br.length, io.ErrUnexpectedEOF)
		}
	} else if resp.ContentLength >= 0 && got < resp.ContentLength {
		return fmt.Errorf("hls segment %d: truncated body %d/%d: %w",
			seg.Index, got, resp.ContentLength, io.ErrUnexpectedEOF)
	}

	// Private key table — the fetch closure flips back to the interruptible
	// network phase so a key miss stays stall-visible.
	sess.setPhase(hlsPhaseDecrypt)
	keyTable, kerr := e.keys.keyTable(&seg, keys, func(uri string) ([aes.BlockSize]byte, error) {
		var zero [aes.BlockSize]byte
		if err := e.waitHostPenalty(sess, at, uri); err != nil {
			return zero, err
		}
		sess.setPhase(hlsPhaseNetwork)
		key, ferr := fetchHLSKey(at.segCtx, e.client, seg.Index, uri,
			e.taskHeaders, e.credBase, e.runCfg, at)
		sess.setPhase(hlsPhaseDecrypt)
		if ferr == nil {
			e.hostLimiter.RecordSuccess(transport.MirrorHost(uri))
		}
		return key, ferr
	})
	if kerr != nil {
		return kerr
	}
	plain, padLen, derr := decryptSegmentInPlace(buf, &seg, keyTable)
	if derr != nil {
		return derr
	}
	if padLen > 0 {
		// Single pad correction: counters and credit move together so the
		// credit now equals the plaintext length.
		e.prog.Bytes.Downloaded.Add(-int64(padLen))
		e.prog.Bytes.VerifiedProgress.Add(-int64(padLen))
		at.credited -= int64(padLen)
	}

	// Pre-commit cancel check — a recorded kill/drain wins over Ingest.
	if at.segCtx.Err() != nil {
		return at.segCtx.Err()
	}
	if !sess.enterCommit(at) {
		return context.Canceled
	}
	commitStart := e.now()
	res, ierr := e.window.Ingest(seg.Index, plain)
	commitDur := e.now().Sub(commitStart)
	sess.speedMu.Lock()
	sess.excludeLocked(commitDur)
	sess.speedMu.Unlock()
	at.noteActivity()
	at.ingestRes = res

	switch {
	case res.Accepted:
		at.accepted = true
		if ierr != nil {
			return ierr
		}
		return nil
	case res.Duplicate && ierr == nil:
		at.duplicate = true
		return nil
	case ierr != nil:
		return ierr
	default:
		return newHLSContractError("segment %d ingest returned indecisive result", seg.Index)
	}
}
