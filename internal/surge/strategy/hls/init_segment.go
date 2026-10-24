package hls

import (
	"context"
	"crypto/aes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// hlsInitSegmentIndex labels init errors — the init segment is not a media
// segment and shares none of the segment retry FSM; failures surface as a
// whole-run error for the scheduler to classify.
const hlsInitSegmentIndex = -1

// initFetchHooks implements hlsAttemptHooks for the init/key fetches:
// activity stamps feed a local idle watchdog and throttle passes through
// the shared task byte limiter while marking the block as protected so the
// watchdog never reads a limiter/host wait as a network stall. Init bytes
// never reach media progress accounting — the plaintext length joins the
// merged/verified counters exactly once after a successful write.
type initFetchHooks struct {
	limiter      types.ByteLimiter
	lastActivity atomic.Int64
	waiting      atomic.Bool
}

func (h *initFetchHooks) noteActivity()  { h.lastActivity.Store(time.Now().UnixNano()) }
func (h *initFetchHooks) noteStatus(int) {}
func (h *initFetchHooks) throttle(ctx context.Context, n int64) error {
	if h.limiter == nil || n <= 0 {
		return nil
	}
	h.noteActivity()
	h.waiting.Store(true)
	defer h.waiting.Store(false)
	err := h.limiter.WaitN(ctx, n)
	h.noteActivity()
	return err
}

// hostLimiter returns the shared limiter for penalty bookkeeping.
func (d *HlsDownloader) hostLimiter() *transport.HostRateLimiter {
	if d.HostLimiter != nil {
		return d.HostLimiter
	}
	return transport.DefaultHostRateLimiter
}

// waitInitPenalty sleeps out the resource host's penalty under ctx,
// marked protected so the idle watchdog ignores it.
func (d *HlsDownloader) waitInitPenalty(ctx context.Context, hooks *initFetchHooks, rawURL string) error {
	until := d.hostLimiter().BlockedUntil(transport.MirrorHost(rawURL), time.Now())
	blocked := time.Until(until)
	if blocked <= 0 {
		return nil
	}
	hooks.waiting.Store(true)
	defer hooks.waiting.Store(false)
	t := time.NewTimer(blocked)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// prepareInitSegment guarantees the physical init bytes at the head of the
// mounted .surge before the window opens. A trusted init (completed flag,
// positive length, prefix file long enough) is skipped; everything else
// refetches under the gated anchor and rewrites offset 0. On success the
// plaintext length joins MergedBytes/VerifiedBytes once — before the
// progress base is set, so no double counting is possible.
func (d *HlsDownloader) prepareInitSegment(ctx context.Context, mnt *hlsMount) error {
	s := mnt.seed
	if s.InitSegmentURI == "" {
		return nil
	}
	// prevInit is the recorded head length the merged media prefix was
	// anchored to — a rewritten init of a different length shifts that
	// boundary and voids the prefix.
	prevInit := s.InitBytes
	if s.InitCompleted && s.InitBytes > 0 {
		if fi, err := mnt.out.Stat(); err != nil {
			return newStorageError("stat", mnt.surgePath, err)
		} else if fi.Size() >= s.InitBytes {
			return nil
		}
		// The claimed init is not backed by the file — refetch and let the
		// accounting below rewrite it consistently.
		s.InitCompleted = false
	}
	if s.InitBytes > 0 {
		// Strip the previous head claim once: the write below adds exactly
		// one init back, so same-length rewrites keep MergedBytes stable.
		s.MergedBytes -= s.InitBytes
		if s.MergedBytes < 0 {
			s.MergedBytes = 0
		}
		s.VerifiedBytes = s.MergedBytes
		s.InitBytes = 0
	}

	credBase, err := parseHLSBinaryURL(s.ManifestURL)
	if err != nil {
		return err
	}
	client, release := newHLSClient(d.Client, &d.runCfg, credBase)
	if release != nil {
		defer release()
	}

	plain, err := d.fetchInitSegment(ctx, client, s, credBase)
	if err != nil {
		return err
	}
	if len(plain) == 0 {
		return newSegmentDataError(hlsInitSegmentIndex, "init body is empty")
	}
	if s.MergedSegments > 0 && int64(len(plain)) != prevInit {
		// Media merged under an init we cannot prove at this length — the
		// prefix is unverifiable, restart it at the new boundary.
		clearMediaPrefix(s)
		s.MergedBytes = 0
		s.VerifiedBytes = 0
	}

	if _, err := mnt.out.Seek(0, io.SeekStart); err != nil {
		return newStorageError("seek", mnt.surgePath, err)
	}
	if err := writeFull(defaultFileOps(), mnt.out, "init write", mnt.surgePath, plain); err != nil {
		return err
	}
	s.InitBytes = int64(len(plain))
	s.MergedBytes += s.InitBytes
	s.VerifiedBytes = s.MergedBytes
	s.InitCompleted = true
	return nil
}

// fetchInitSegment performs the gated init GET plus optional key fetch and
// in-place decrypt, returning the plaintext. One bounded idle watchdog
// covers TTFB and body reads with the same default stall window as the
// engine; limiter and host-penalty waits are marked protected.
func (d *HlsDownloader) fetchInitSegment(ctx context.Context, client *http.Client, s *types.HLSResumeState, credBase *url.URL) ([]byte, error) {
	var br *hlsByteRange
	switch {
	case s.InitByteLength == 0 && s.InitByteOffset == 0:
	case s.InitByteLength <= 0 || s.InitByteOffset < 0 ||
		s.InitByteLength > math.MaxInt64-s.InitByteOffset:
		return nil, newHLSContractError("init byte range %d@%d invalid", s.InitByteLength, s.InitByteOffset)
	default:
		br = &hlsByteRange{offset: s.InitByteOffset, length: s.InitByteLength}
	}

	hooks := &initFetchHooks{limiter: d.Limiter}
	hooks.noteActivity()

	ictx, cancel := context.WithCancel(ctx)
	defer cancel()
	if stall := d.runCfg.GetStallTimeout(); stall > 0 {
		var wg sync.WaitGroup
		wg.Go(func() {
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ictx.Done():
					return
				case <-tick.C:
					if hooks.waiting.Load() {
						continue
					}
					if la := hooks.lastActivity.Load(); la > 0 &&
						time.Since(time.Unix(0, la)) >= stall {
						cancel()
						return
					}
				}
			}
		})
		defer func() { cancel(); wg.Wait() }()
	}

	if err := d.waitInitPenalty(ictx, hooks, s.InitSegmentURI); err != nil {
		return nil, err
	}
	req, err := newHLSSubRequest(ictx, s.InitSegmentURI, d.taskHeaders, credBase, &d.runCfg, br)
	if err != nil {
		return nil, err
	}
	hooks.noteActivity()
	resp, err := client.Do(req)
	if err != nil {
		return nil, &hlsTransportError{err: err}
	}
	hooks.noteActivity()
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*utils.KiB))
		_ = resp.Body.Close()
	}()

	expected := http.StatusOK
	if br != nil {
		expected = http.StatusPartialContent
	}
	if resp.StatusCode != expected {
		if resp.StatusCode >= 400 {
			se := &hlsStatusError{
				resource:   "init",
				index:      hlsInitSegmentIndex,
				status:     resp.StatusCode,
				retryAfter: resp.Header.Get("Retry-After"),
				host:       transport.MirrorHost(s.InitSegmentURI),
			}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
				if ra, ok := hlsValidRetryAfter(se.retryAfter, time.Now()); ok {
					d.hostLimiter().Penalize(se.host, ra, true, time.Now())
				}
				return nil, se
			}
			if types.IsPermanentHTTPStatus(resp.StatusCode) {
				return nil, errors.Join(se, types.ErrPermanentHTTP)
			}
			return nil, se
		}
		return nil, fmt.Errorf("hls init: unexpected status %d: %w",
			resp.StatusCode, types.ErrHLSInvalidSegmentResponse)
	}
	if err := validateSegmentResponse(resp, br, hlsInitSegmentIndex); err != nil {
		return nil, err
	}
	d.hostLimiter().RecordSuccess(transport.MirrorHost(s.InitSegmentURI))

	buf := hlsBufGet()
	defer func() { hlsBufPut(buf) }()
	for {
		want := hlsWorkerBatchSize
		if br != nil {
			remaining := br.length + 1 - int64(len(buf))
			if remaining <= 0 {
				break
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
			hooks.noteActivity()
			if terr := hooks.throttle(ictx, int64(n)); terr != nil {
				return nil, terr
			}
			hooks.noteActivity()
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("hls init: body read: %w", rerr)
		}
		if n == 0 {
			return nil, fmt.Errorf("hls init: body read: %w", io.ErrNoProgress)
		}
	}
	got := int64(len(buf))
	if br != nil {
		if got > br.length {
			return nil, fmt.Errorf("hls init: body exceeds range length: %w", types.ErrHLSInvalidSegmentResponse)
		}
		if got < br.length {
			return nil, fmt.Errorf("hls init: truncated body %d/%d: %w", got, br.length, io.ErrUnexpectedEOF)
		}
	} else if resp.ContentLength >= 0 && got < resp.ContentLength {
		return nil, fmt.Errorf("hls init: truncated body %d/%d: %w", got, resp.ContentLength, io.ErrUnexpectedEOF)
	}

	// Init decryption: the init descriptor carries its own KeyIndex/IV —
	// never a media Sequence. An encrypted init without an explicit IV
	// fails before any byte reaches the file. The pooled buffer is recycled
	// on return, so the plaintext escapes as an exact-size copy.
	if s.InitKeyIndex == 0 {
		return slices.Clone(buf), nil
	}
	if s.InitKeyIndex < 0 || s.InitKeyIndex > len(s.Keys) {
		return nil, newSegmentDataError(hlsInitSegmentIndex, "init key index %d out of range", s.InitKeyIndex)
	}
	if s.InitIVHex == "" {
		return nil, newSegmentDataError(hlsInitSegmentIndex, "encrypted init has no explicit IV")
	}
	tmp := &types.HLSSegmentState{
		Index:    hlsInitSegmentIndex,
		KeyIndex: s.InitKeyIndex,
		IVHex:    s.InitIVHex,
	}
	keys := newHLSKeyCache()
	table, err := keys.keyTable(tmp, s.Keys, func(uri string) ([aes.BlockSize]byte, error) {
		var zero [aes.BlockSize]byte
		if err := d.waitInitPenalty(ictx, hooks, uri); err != nil {
			return zero, err
		}
		key, ferr := fetchHLSKey(ictx, client, hlsInitSegmentIndex, uri, d.taskHeaders, credBase, &d.runCfg, hooks)
		if ferr == nil {
			d.hostLimiter().RecordSuccess(transport.MirrorHost(uri))
		}
		return key, ferr
	})
	if err != nil {
		return nil, err
	}
	plain, _, err := decryptSegmentInPlace(buf, tmp, table)
	if err != nil {
		return nil, err
	}
	return slices.Clone(plain), nil
}
