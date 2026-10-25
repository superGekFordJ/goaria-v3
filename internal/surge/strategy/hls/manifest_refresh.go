package hls

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// hlsRefreshCooldown throttles the shared manifest refresh: the next
// flight may start only this long after the previous fetch completed,
// whether it succeeded or failed. A zero lastRefreshAt leaves the first
// 403/410 free to refresh immediately.
const hlsRefreshCooldown = 10 * time.Second

// fetchRefreshPlaylist re-walks the manifest chain under the caller's
// already-bounded context: a still-usable MediaPlaylistURL first, the
// manifest anchor otherwise. A master document selects the best muxed
// variant and fetches its media playlist under the same budget — a nested
// master is rejected like before. When the media entry exists but fails at
// request level and differs from the anchor, exactly one fallback re-walks
// from ManifestURL; a fetched-but-incompatible document is never an entry
// failure, so permanent gates (live, DRM, demuxed audio, malformed) can
// not be bypassed by switching entries.
func fetchRefreshPlaylist(
	ctx context.Context,
	client *http.Client,
	seed *types.HLSResumeState,
	headers map[string]string,
	credBase *url.URL,
	runCfg *types.RuntimeConfig,
) (*MediaPlaylist, int64, error) {
	// walk performs the entry fetch plus the optional master second hop.
	// fetchFailed marks request-level failure (transport or non-200), the
	// only class an entry fallback may retry elsewhere.
	walk := func(entry string) (pl *MediaPlaylist, bandwidth int64, fetchFailed bool, err error) {
		text, finalURL, _, err := fetchManifestText(ctx, client, entry, headers, credBase, runCfg)
		if err != nil {
			return nil, 0, true, err
		}
		kind, masterPl, mediaPl, err := ParsePlaylist(text, finalURL)
		if err != nil {
			return nil, 0, false, err
		}
		if kind == PlaylistKindMaster {
			best, serr := SelectBestVariant(masterPl)
			if serr != nil {
				return nil, 0, false, serr
			}
			bandwidth = best.Bandwidth
			body, mediaFinal, _, ferr := fetchManifestText(ctx, client, best.URI, headers, credBase, runCfg)
			if ferr != nil {
				return nil, 0, true, ferr
			}
			kind2, _, mediaPl2, perr := ParsePlaylist(body, mediaFinal)
			if perr != nil {
				return nil, 0, false, perr
			}
			if kind2 != PlaylistKindMedia {
				return nil, 0, false, fmt.Errorf("%w: nested master playlist is not allowed", types.ErrHLSInvalidManifest)
			}
			mediaPl = mediaPl2
		}
		if mediaPl == nil {
			return nil, 0, false, fmt.Errorf("%w: no media playlist", types.ErrHLSInvalidManifest)
		}
		return mediaPl, bandwidth, false, nil
	}

	anchor := strings.TrimSpace(seed.ManifestURL)
	start := strings.TrimSpace(seed.MediaPlaylistURL)
	if u, err := url.Parse(start); err != nil || u == nil || u.Host == "" ||
		(!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		start = anchor
	}
	mediaPl, bandwidth, fetchFailed, err := walk(start)
	if err != nil && fetchFailed && anchor != "" && start != anchor {
		mediaPl, bandwidth, _, err = walk(anchor)
	}
	return mediaPl, bandwidth, err
}

// validateRefreshPlaylist proves a freshly parsed media playlist describes
// the same VOD as the prepared seed — identical non-URI structure — before
// any URI may move. Every check completes before a single write happens;
// a failure keeps ErrSourceMetadataMismatch identity so the caller and the
// scheduler both read it as terminal. A non-empty stored fingerprint must
// equal the parser's normalized fingerprint exactly: percent-encoding
// distinctions such as %7E vs ~ are intentional identity, not aliases. An
// empty stored fingerprint still requires every structural check to pass;
// the first successful parse then adopts the new fingerprint rather than
// comparing against the empty string.
func validateRefreshPlaylist(seed *types.HLSResumeState, mediaPl *MediaPlaylist) error {
	mismatch := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{types.ErrSourceMetadataMismatch}, args...)...)
	}
	if seed == nil || mediaPl == nil {
		return mismatch("missing playlist to compare")
	}
	if len(mediaPl.Segments) != seed.TotalSegments {
		return mismatch("segment count %d != %d", len(mediaPl.Segments), seed.TotalSegments)
	}
	if mediaPl.ContainerFormat != seed.ContainerFormat {
		return mismatch("container %q != %q", mediaPl.ContainerFormat, seed.ContainerFormat)
	}
	for i := range seed.Segments {
		old, fresh := &seed.Segments[i], &mediaPl.Segments[i]
		if fresh.Index != old.Index || fresh.Sequence != old.Sequence ||
			fresh.DurationMicros != old.DurationMicros ||
			fresh.ByteOffset != old.ByteOffset || fresh.ByteLength != old.ByteLength ||
			fresh.KeyIndex != old.KeyIndex || fresh.IVHex != old.IVHex {
			return mismatch("segment %d descriptor changed", i)
		}
	}
	if len(mediaPl.Keys) != len(seed.Keys) {
		return mismatch("key count %d != %d", len(mediaPl.Keys), len(seed.Keys))
	}
	for i := range seed.Keys {
		if mediaPl.Keys[i].Method != seed.Keys[i].Method || mediaPl.Keys[i].IVHex != seed.Keys[i].IVHex {
			return mismatch("key %d descriptor changed", i)
		}
	}
	if (mediaPl.InitMap == nil) != (seed.InitSegmentURI == "") {
		return mismatch("init map presence changed")
	}
	if mediaPl.InitMap != nil &&
		(mediaPl.InitMap.ByteOffset != seed.InitByteOffset ||
			mediaPl.InitMap.ByteLength != seed.InitByteLength ||
			mediaPl.InitMap.KeyIndex != seed.InitKeyIndex ||
			mediaPl.InitMap.IVHex != seed.InitIVHex) {
		return mismatch("init descriptor changed")
	}
	if seed.ManifestFingerprint != "" && mediaPl.ManifestFingerprint != seed.ManifestFingerprint {
		return mismatch("fingerprint changed")
	}
	return nil
}

// applyManifestURIs rewrites dst's signed URI surface from a
// compatibility-checked media playlist: fingerprint, media playlist
// anchor, segment reference, init URI, every key URI, and the segment
// URIs at or beyond fromSeg. The merged prefix below fromSeg is never
// fetched again and keeps its recorded URIs; every non-URI descriptor,
// progress counter, bitmap and spill ledger field is preserved.
// ManifestURL is the identity anchor and never moves.
func applyManifestURIs(dst *types.HLSResumeState, mediaPl *MediaPlaylist, fromSeg int) {
	dst.ManifestFingerprint = mediaPl.ManifestFingerprint
	dst.MediaPlaylistURL = mediaPl.BaseURL
	dst.SegmentRefURL = mediaPl.SegmentRefURL
	if mediaPl.InitMap != nil {
		dst.InitSegmentURI = mediaPl.InitMap.URI
	}
	for i := range dst.Keys {
		dst.Keys[i].URI = mediaPl.Keys[i].URI
	}
	for i := fromSeg; i < dst.TotalSegments && i < len(dst.Segments); i++ {
		dst.Segments[i].URI = mediaPl.Segments[i].URI
	}
}

// refreshFatalCause reports the terminal cause when a manifest refresh
// failed permanently — a fingerprint/structure mismatch, a permanent HLS
// gate, a credential/redirect refusal or a permanent HTTP status — so the
// engine latches the real identity instead of burning the segment retry
// budget on dead URIs. Transient request failures (timeouts, resets,
// 5xx) return nil: the old URIs stay in service and the refresh marks
// survive for the next post-cooldown candidate.
func refreshFatalCause(err error) error {
	switch {
	case errors.Is(err, types.ErrSourceMetadataMismatch),
		types.IsPermanentHTTPError(err),
		types.IsPermanentHLSError(err),
		errors.Is(err, types.ErrMaxRedirects),
		errors.Is(err, errRedirectUserinfo):
		return err
	}
	var contractErr *hlsContractError
	var refusalErr *hlsRedirectRefusalError
	if errors.As(err, &contractErr) || errors.As(err, &refusalErr) {
		return err
	}
	return nil
}

// attemptURISnapshot copies — under refreshMu then q.mu — the segment
// descriptor and key table one attempt may see, and stamps the task's
// seen generation with the engine generation that produced them. A single
// critical section keeps the URI and the key table generation-consistent:
// a refresh adopting between two separate reads can never mix
// generations inside one attempt.
func (e *segmentEngine) attemptURISnapshot(task *segmentTask) (types.HLSSegmentState, []types.HLSKeyInfo) {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	e.queue.mu.Lock()
	defer e.queue.mu.Unlock()
	task.lastSeenRefreshGen = e.refreshGen
	return task.seg, slices.Clone(e.seed.Keys)
}

// refreshManifestOnAuthExpired consumes a task's 403/410 refresh mark
// before its next media request. Decision order under refreshMu then
// q.mu: a cancelled caller or engine exits; an unmarked task returns; an
// engine generation newer than the task's last used snapshot consumes
// the mark lazily; a live flight is awaited on its done channel or the
// attempt's own segCtx — never a peer's; inside the cooldown the mark
// stays for the next post-window candidate and this attempt reuses the
// current URI. Otherwise the caller starts exactly one shared flight on
// e.ctx + PreflightTimeout — bound to no worker segCtx — then waits like
// every other candidate so a kill/drain can never strand it inside the
// fetch. The wait projects as hlsPhaseWait: kill and drain interrupt it,
// stall can not.
func (e *segmentEngine) refreshManifestOnAuthExpired(sess *hlsWorkerSession, at *hlsAttempt) {
	if at.segCtx.Err() != nil || e.ctx.Err() != nil {
		return
	}
	var done chan struct{}
	e.refreshMu.Lock()
	e.queue.mu.Lock()
	switch {
	case !at.task.refreshRequired:
	case e.refreshGen > at.task.lastSeenRefreshGen:
		// Another worker already adopted a generation newer than the one
		// this task's failing request used — consume the mark and retry
		// the refreshed descriptor without a new fetch.
		at.task.refreshRequired = false
		at.task.lastSeenRefreshGen = e.refreshGen
	case e.refreshInFlight:
		done = e.refreshDone
	case e.now().Sub(e.lastRefreshAt) >= hlsRefreshCooldown:
		e.refreshInFlight = true
		done = make(chan struct{})
		e.refreshDone = done
		e.refreshWG.Add(1)
		go e.runManifestRefresh(e.seed.Clone(), done)
	default:
		// Cooldown: keep the mark; this attempt retries the current URI
		// inside the ordinary budget — no extra sleep, no busy loop.
	}
	e.queue.mu.Unlock()
	e.refreshMu.Unlock()
	if done == nil {
		return
	}
	sess.setPhase(hlsPhaseWait)
	at.noteActivity()
	select {
	case <-done:
	case <-at.segCtx.Done():
	}
	at.noteActivity()
	sess.setPhase(hlsPhaseNetwork)
}

// runManifestRefresh performs the single shared manifest fetch of one
// flight. The seed is a detached clone taken under refreshMu so no live
// table is handed to lock-free parsing. A playlist that validates while
// the engine is still live adopts every URI surface under the fixed
// refreshMu→q.mu→w.mu order and publishes the next generation; terminal
// causes latch outside the locks; transient failures only close the
// flight — the marks stay set and waiters continue with ordinary retries.
// The flight is scored at fetch completion, success or failure alike.
func (e *segmentEngine) runManifestRefresh(seedSnap *types.HLSResumeState, done chan struct{}) {
	defer e.refreshWG.Done()
	defer func() {
		e.refreshMu.Lock()
		e.lastRefreshAt = e.now()
		e.refreshInFlight = false
		close(done)
		e.refreshMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(e.ctx, PreflightTimeout)
	mediaPl, _, err := fetchRefreshPlaylist(ctx, e.client, seedSnap, e.taskHeaders, e.credBase, e.runCfg)
	cancel()

	var fatalErr error
	if err == nil {
		if verr := validateRefreshPlaylist(seedSnap, mediaPl); verr != nil {
			fatalErr = verr
		} else if e.ctx.Err() == nil {
			e.adoptRefreshURIs(mediaPl)
		}
	} else {
		fatalErr = refreshFatalCause(err)
	}
	if fatalErr != nil {
		e.latchFatal(fatalErr)
	} else if err != nil && e.ctx.Err() == nil {
		utils.Debug("hls: manifest refresh failed, keeping current URIs: %v", err)
	}
}

// adoptRefreshURIs publishes a validated manifest's URI surface under the
// fixed refreshMu→q.mu→w.mu order: the window seed, the engine seed and
// every unfinished queue task move to the new signed URIs bounded by the
// window's live merged frontier — never a construction-time bitmap —
// then the generation publishes. Satisfied refresh marks clear;
// lastSeenRefreshGen is left alone: it records which generation each
// task's last request actually used, and a late old-generation 403 must
// not be misread as the new URI failing.
func (e *segmentEngine) adoptRefreshURIs(mediaPl *MediaPlaylist) {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	e.queue.mu.Lock()
	defer e.queue.mu.Unlock()
	e.window.refreshManifestURIs(e.seed, mediaPl)
	for idx, task := range e.queue.tasks {
		if e.queue.states[idx] != segDone {
			task.seg.URI = mediaPl.Segments[task.seg.Index].URI
		}
		task.refreshRequired = false
	}
	e.refreshGen++
}
