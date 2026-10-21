// Extension request routing, async extractor resolve/batch dispatching, direct batch download pipeline, capability negotiation, and linkage error redaction.

package extension

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

func (s *Server) dispatchDownload(sc *safeConn, env RequestEnvelope, raw []byte) {
	var req DownloadRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		resp := DownloadResponse{Type: MsgTypeDownloadAck, Success: false, Error: "invalid request"}
		if validRequestID(env.RequestID) {
			resp.RequestID = env.RequestID
		}
		_ = sc.writeJSON(resp)
		return
	}
	resp := s.handleDownload(req)
	if validRequestID(req.RequestID) {
		resp.RequestID = req.RequestID
	} else if validRequestID(env.RequestID) {
		resp.RequestID = env.RequestID
	}
	_ = sc.writeJSON(resp)
}

func (s *Server) dispatchAsync(
	opCtx context.Context,
	connCtx context.Context,
	sc *safeConn,
	env RequestEnvelope,
	raw []byte,
	ackType string,
	requiredCap string,
	isResolve bool,
) {
	reqID := env.RequestID
	if !validRequestID(reqID) {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: echoRequestID(reqID), ErrorCode: ErrCodeInvalidRequest})
		return
	}
	if !sc.hasGranted(requiredCap) {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeUnavailable})
		return
	}
	// Reject unsolicited browser header grants before idempotency begins so a
	// same-request_id retry without grants still resolves normally.
	if isResolve && !sc.hasGranted(CapExtractorHeaderContext) && rawHasBrowserHeaderGrants(raw) {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeInvalidRequest})
		return
	}
	digest := canonicalDigest(raw)
	linkage, gen := sc.snapshotLinkage()
	st, cached, wait := s.idemp.lookup(gen, env.Type, reqID, digest)
	switch st {
	case idempHit:
		if isResolve && linkage.Resolver != nil {
			if rewritten := linkage.Resolver.RewriteCachedResolve(cached); len(rewritten) > 0 {
				cached = rewritten
			}
		}
		_ = sc.writeRaw(cached)
		return
	case idempConflict:
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeIdempotencyConflict})
		return
	case idempCoalesce:
		go writeCoalescedAck(sc, wait)
		return
	case idempBusy:
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}

	if !sc.tryAcquireInFlight() {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}
	if !s.tryAcquireGate(isResolve) {
		sc.releaseInFlight()
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}

	st, cached, wait = s.idemp.begin(gen, env.Type, reqID, digest)
	switch st {
	case idempHit:
		s.releaseGate(isResolve)
		sc.releaseInFlight()
		if isResolve && linkage.Resolver != nil {
			if rewritten := linkage.Resolver.RewriteCachedResolve(cached); len(rewritten) > 0 {
				cached = rewritten
			}
		}
		_ = sc.writeRaw(cached)
		return
	case idempConflict:
		s.releaseGate(isResolve)
		sc.releaseInFlight()
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeIdempotencyConflict})
		return
	case idempCoalesce:
		s.releaseGate(isResolve)
		sc.releaseInFlight()
		go writeCoalescedAck(sc, wait)
		return
	case idempBusy:
		s.releaseGate(isResolve)
		sc.releaseInFlight()
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}

	go func() {
		defer s.releaseGate(isResolve)
		defer sc.releaseInFlight()
		defer func() {
			if rec := recover(); rec != nil {
				func() {
					defer func() { _ = recover() }()
					log.Printf("[Extension] async handler panic: %T", rec)
					busy := marshalBusyAck(ackType, reqID)
					s.idemp.abandon(gen, env.Type, reqID, digest, busy)
					if connCtx.Err() == nil {
						_ = sc.writeRaw(busy)
					}
				}()
			}
		}()
		var data []byte
		var err error
		skipIdemp := false
		if isResolve {
			if linkage.Resolver == nil || !linkage.Resolver.Ready() {
				unavail := marshalUnavailableAck(ackType, reqID)
				s.idemp.abandon(gen, env.Type, reqID, digest, unavail)
				if connCtx.Err() == nil {
					_ = sc.writeRaw(unavail)
				}
				return
			}
			result := linkage.Resolver.HandleResolve(WithHeaderContextGrant(opCtx, sc.hasGranted(CapExtractorHeaderContext)), env, json.RawMessage(raw))
			if opCtx.Err() != nil {
				busy := marshalBusyAck(ackType, reqID)
				s.idemp.abandon(gen, env.Type, reqID, digest, busy)
				if connCtx.Err() == nil {
					_ = sc.writeRaw(busy)
				}
				return
			}
			ack := s.resolveAckFromResult(ackType, reqID, result)
			data, err = json.Marshal(ack)
		} else {
			if linkage.Committer == nil || !linkage.Committer.Ready() {
				unavail := marshalUnavailableAck(ackType, reqID)
				s.idemp.abandon(gen, env.Type, reqID, digest, unavail)
				if connCtx.Err() == nil {
					_ = sc.writeRaw(unavail)
				}
				return
			}
			result := linkage.Committer.HandleCommit(opCtx, env, json.RawMessage(raw))
			if opCtx.Err() != nil {
				busy := marshalBusyAck(ackType, reqID)
				s.idemp.abandon(gen, env.Type, reqID, digest, busy)
				if connCtx.Err() == nil {
					_ = sc.writeRaw(busy)
				}
				return
			}
			skipIdemp = result.SkipIdempotency
			data, err = marshalBatchAck(s.batchAckFromResult(ackType, reqID, result))
		}
		if err != nil {
			busy := marshalBusyAck(ackType, reqID)
			s.idemp.abandon(gen, env.Type, reqID, digest, busy)
			if connCtx.Err() == nil {
				_ = sc.writeRaw(busy)
			}
			return
		}
		if skipIdemp || opCtx.Err() != nil {
			s.idemp.abandon(gen, env.Type, reqID, digest, data)
		} else {
			s.idemp.complete(gen, env.Type, reqID, digest, data)
		}
		if connCtx.Err() == nil {
			_ = sc.writeRaw(data)
		}
	}()
}

func writeCoalescedAck(sc *safeConn, wait <-chan []byte) {
	if wait == nil {
		return
	}
	ack, ok := <-wait
	if !ok || len(ack) == 0 {
		return
	}
	_ = sc.writeRaw(ack)
}

func marshalTypedAck(ackType, requestID, errCode string) []byte {
	ack := TypedAck{Type: ackType, RequestID: requestID, ErrorCode: errCode}
	data, err := json.Marshal(ack)
	if err != nil {
		return []byte(`{"type":` + strconv.Quote(ackType) + `,"request_id":` + strconv.Quote(requestID) + `,"error_code":` + strconv.Quote(errCode) + `}`)
	}
	return data
}

func marshalBusyAck(ackType, requestID string) []byte {
	return marshalTypedAck(ackType, requestID, ErrCodeBusy)
}

func marshalUnavailableAck(ackType, requestID string) []byte {
	return marshalTypedAck(ackType, requestID, ErrCodeUnavailable)
}

func (s *Server) dispatchDirectBatch(
	opCtx context.Context,
	connCtx context.Context,
	sc *safeConn,
	env RequestEnvelope,
	raw []byte,
) {
	ackType := MsgTypeDownloadBatchAck
	reqID := env.RequestID
	if !validRequestID(reqID) {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: echoRequestID(reqID), ErrorCode: ErrCodeInvalidRequest})
		return
	}
	if !sc.hasGranted(CapDownloadBatch) {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeUnavailable})
		return
	}
	parsed, errCode := ParseDirectBatchRequest(raw)
	if errCode != "" {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeInvalidRequest})
		return
	}
	reqID = parsed.RequestID

	digest := canonicalDigest(raw)
	gen := uint64(0)
	if s.store != nil {
		gen = s.store.Generation()
	}
	st, cached, wait := s.idemp.lookup(gen, env.Type, reqID, digest)
	switch st {
	case idempHit:
		_ = sc.writeRaw(cached)
		return
	case idempConflict:
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeIdempotencyConflict})
		return
	case idempCoalesce:
		go writeCoalescedAck(sc, wait)
		return
	case idempBusy:
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}

	if !sc.tryAcquireInFlight() {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}
	if !s.tryAcquireGate(false) {
		sc.releaseInFlight()
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}

	st, cached, wait = s.idemp.begin(gen, env.Type, reqID, digest)
	switch st {
	case idempHit:
		s.releaseGate(false)
		sc.releaseInFlight()
		_ = sc.writeRaw(cached)
		return
	case idempConflict:
		s.releaseGate(false)
		sc.releaseInFlight()
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeIdempotencyConflict})
		return
	case idempCoalesce:
		s.releaseGate(false)
		sc.releaseInFlight()
		go writeCoalescedAck(sc, wait)
		return
	case idempBusy:
		s.releaseGate(false)
		sc.releaseInFlight()
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: reqID, ErrorCode: ErrCodeBusy})
		return
	}

	parsed.PayloadDigest = digest
	s.mu.RLock()
	direct := s.linkage.DirectCommitter
	s.mu.RUnlock()
	if direct != nil && !direct.AdmitPending(reqID, digest) {
		s.releaseGate(false)
		sc.releaseInFlight()
		busy := marshalBusyAck(ackType, reqID)
		s.idemp.abandon(gen, env.Type, reqID, digest, busy)
		_ = sc.writeRaw(busy)
		return
	}

	go func() {
		defer s.releaseGate(false)
		defer sc.releaseInFlight()
		defer func() {
			if rec := recover(); rec != nil {
				func() {
					defer func() { _ = recover() }()
					log.Printf("[Extension] async handler panic: %T", rec)
					if direct != nil {
						direct.AbandonPending(reqID)
					}
					busy := marshalBusyAck(ackType, reqID)
					s.idemp.abandon(gen, env.Type, reqID, digest, busy)
					if connCtx.Err() == nil {
						_ = sc.writeRaw(busy)
					}
				}()
			}
		}()
		result := s.runDirectBatch(opCtx, RequestEnvelope{Type: env.Type, RequestID: parsed.RequestID}, parsed)
		if opCtx.Err() != nil {
			if direct != nil && (result.SkipIdempotency || result.ErrorCode == ErrCodeBusy) {
				direct.AbandonPending(reqID)
			}
			busy := marshalBusyAck(ackType, reqID)
			s.idemp.abandon(gen, env.Type, reqID, digest, busy)
			if connCtx.Err() == nil {
				_ = sc.writeRaw(busy)
			}
			return
		}
		data, err := marshalDirectBatchResult(ackType, reqID, result)
		if err != nil {
			busy := marshalBusyAck(ackType, reqID)
			s.idemp.abandon(gen, env.Type, reqID, digest, busy)
			if connCtx.Err() == nil {
				_ = sc.writeRaw(busy)
			}
			return
		}
		if result.SkipIdempotency {
			s.idemp.abandon(gen, env.Type, reqID, digest, data)
		} else {
			s.idemp.complete(gen, env.Type, reqID, digest, data)
		}
		if connCtx.Err() == nil {
			_ = sc.writeRaw(data)
		}
	}()
}

func (s *Server) dispatchDirectBatchStatus(sc *safeConn, env RequestEnvelope, raw []byte) {
	ackType := MsgTypeDownloadBatchStatusAck
	reqID := env.RequestID
	if !sc.hasGranted(CapDownloadBatch) {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: echoRequestID(reqID), ErrorCode: ErrCodeUnavailable})
		return
	}
	parsedID, errCode := ParseDirectBatchStatusRequest(raw)
	if errCode != "" {
		_ = sc.writeJSON(TypedAck{Type: ackType, RequestID: echoRequestID(reqID), ErrorCode: ErrCodeInvalidRequest})
		return
	}
	s.mu.RLock()
	d := s.linkage.DirectCommitter
	s.mu.RUnlock()
	if d == nil {
		_ = sc.writeJSON(DirectBatchStatusAck{
			Type:      ackType,
			RequestID: parsedID,
			Status:    DirectBatchStatusNotFound,
		})
		return
	}
	snap, ok := d.LookupStatus(parsedID)
	if !ok {
		_ = sc.writeJSON(DirectBatchStatusAck{
			Type:      ackType,
			RequestID: parsedID,
			Status:    DirectBatchStatusNotFound,
		})
		return
	}
	ack := DirectBatchStatusAck{
		Type:      ackType,
		RequestID: parsedID,
		Status:    snap.Status,
	}
	if snap.Status == DirectBatchStatusComplete {
		ack.Success = snap.Success
		ack.GroupKey = snap.GroupKey
		ack.SucceededItemIDs = snap.SucceededItemIDs
		if ack.SucceededItemIDs == nil {
			ack.SucceededItemIDs = []string{}
		}
		ack.DuplicateItemIDs = snap.DuplicateItemIDs
		if ack.DuplicateItemIDs == nil {
			ack.DuplicateItemIDs = []string{}
		}
		if snap.ErrorsByItemID == nil {
			ack.ErrorsByItemID = map[string]string{}
		} else {
			ack.ErrorsByItemID = SanitizeCommitItemErrors(snap.ErrorsByItemID)
		}
	}
	_ = sc.writeJSON(ack)
}

func (s *Server) runDirectBatch(ctx context.Context, env RequestEnvelope, req DirectBatchRequest) DirectCommitResult {
	s.mu.RLock()
	d := s.linkage.DirectCommitter
	s.mu.RUnlock()
	if d == nil {
		return DirectCommitResult{ErrorCode: ErrCodeUnavailable, SkipIdempotency: true}
	}
	return d.HandleDirectBatch(ctx, env, req)
}

func marshalDirectBatchResult(ackType, requestID string, result DirectCommitResult) ([]byte, error) {
	if result.ErrorCode != "" {
		return json.Marshal(TypedAck{Type: ackType, RequestID: requestID, ErrorCode: result.ErrorCode})
	}
	ack := DirectBatchAck{
		Type:             ackType,
		RequestID:        requestID,
		Success:          result.Success,
		GroupKey:         result.GroupKey,
		SucceededItemIDs: result.SucceededItemIDs,
		DuplicateItemIDs: result.DuplicateItemIDs,
		ErrorsByItemID:   result.ErrorsByItemID,
	}
	if ack.SucceededItemIDs == nil {
		ack.SucceededItemIDs = []string{}
	}
	if ack.DuplicateItemIDs == nil {
		ack.DuplicateItemIDs = []string{}
	}
	if len(ack.ErrorsByItemID) > 0 {
		ack.ErrorsByItemID = SanitizeCommitItemErrors(ack.ErrorsByItemID)
	} else {
		ack.ErrorsByItemID = map[string]string{}
	}
	return json.Marshal(ack)
}

func (s *Server) batchAckFromResult(ackType, requestID string, result CommitResult) BatchDownloadAck {
	ack := BatchDownloadAck{
		Type:      ackType,
		RequestID: requestID,
		Success:   result.Success,
		ErrorCode: result.ErrorCode,
	}
	if result.Error != "" {
		ack.Error = s.redactLinkageError(result.Error)
	}
	if result.ErrorCode != "" {
		return ack
	}
	ack.GroupKey = result.GroupKey
	ack.SucceededItemIDs = result.SucceededItemIDs
	if ack.SucceededItemIDs == nil {
		ack.SucceededItemIDs = []string{}
	}
	ack.DuplicateItemIDs = result.DuplicateItemIDs
	if ack.DuplicateItemIDs == nil {
		ack.DuplicateItemIDs = []string{}
	}
	if len(result.ErrorsByItemID) > 0 {
		ack.ErrorsByItemID = SanitizeCommitItemErrors(result.ErrorsByItemID)
	}

	return ack
}

func marshalBatchAck(ack BatchDownloadAck) ([]byte, error) {
	if ack.ErrorCode != "" {
		return json.Marshal(TypedAck{
			Type:      ack.Type,
			RequestID: ack.RequestID,
			ErrorCode: ack.ErrorCode,
			Error:     ack.Error,
		})
	}
	type successWire struct {
		Type             string            `json:"type"`
		RequestID        string            `json:"request_id,omitempty"`
		Success          bool              `json:"success"`
		GroupKey         string            `json:"group_key,omitempty"`
		SucceededItemIDs []string          `json:"succeeded_item_ids"`
		DuplicateItemIDs []string          `json:"duplicate_item_ids"`
		ErrorsByItemID   map[string]string `json:"errors_by_item_id,omitempty"`
		Error            string            `json:"error,omitempty"`
	}

	return json.Marshal(successWire{
		Type:             ack.Type,
		RequestID:        ack.RequestID,
		Success:          ack.Success,
		GroupKey:         ack.GroupKey,
		SucceededItemIDs: ack.SucceededItemIDs,
		DuplicateItemIDs: ack.DuplicateItemIDs,
		ErrorsByItemID:   ack.ErrorsByItemID,
		Error:            ack.Error,
	})
}

func (s *Server) resolveAckFromResult(ackType, requestID string, result ResolveResult) ExtractorResolveAck {
	ack := ExtractorResolveAck{
		Type:      ackType,
		RequestID: requestID,
		Items:     make([]ExtractorResolveAckItem, 0, len(result.Items)),
	}
	if result.ErrorCode != "" {
		ack.ErrorCode = result.ErrorCode
		if result.Error != "" {
			ack.Error = s.redactLinkageError(result.Error)
		}

		return ack
	}
	matched := result.Matched
	ack.Matched = &matched
	if result.Matched {
		ack.SessionID = result.SessionID
		ack.TotalCount = result.TotalCount
		ack.TotalBytes = result.TotalBytes
	}
	for _, item := range result.Items {
		ack.Items = append(ack.Items, ExtractorResolveAckItem(item))
	}

	return ack
}

func (s *Server) redactLinkageError(message string) string {
	s.mu.RLock()
	redactor := s.linkage.Redactor
	s.mu.RUnlock()
	if redactor == nil {
		return ""
	}

	return redactor.Redact(errors.New(message))
}

func computeConnectionCapabilities(secret string, l Linkage) []string {
	caps := []string{CapRequestID}
	if secret == "" {
		return caps
	}
	if l.Resolver != nil && l.Resolver.Ready() {
		caps = append(caps, CapExtractorResolve)
		if hc, ok := l.Resolver.(HeaderContextResolver); ok && hc.HeaderContextReady() {
			caps = append(caps, CapExtractorHeaderContext)
		}
	}
	if l.Committer != nil && l.Committer.Ready() {
		caps = append(caps, CapExtractorBatch)
	}
	if l.DirectCommitter != nil && l.DirectCommitter.Ready() {
		caps = append(caps, CapDownloadBatch)
	}
	return caps
}

func matchWireFromProvider(caps []string, provider MatchDigestProvider) *MatchDigestWire {
	if !containsCap(caps, CapExtractorResolve) || provider == nil || !provider.Ready() {
		return nil
	}
	snap, ok := provider.Snapshot()
	if !ok {
		return nil
	}
	if snap.Version != MatchDigestVersion || !validMatchSaltHex(snap.Salt) {
		return nil
	}
	exact := snap.ExactDigests
	if exact == nil {
		exact = []string{}
	}
	sub := snap.SubdomainDigests
	if sub == nil {
		sub = []string{}
	}
	return &MatchDigestWire{
		DigestVersion:    snap.Version,
		Salt:             snap.Salt,
		ExactDigests:     exact,
		SubdomainDigests: sub,
	}
}

func containsCap(caps []string, want string) bool {
	return slices.Contains(caps, want)
}

// rawHasBrowserHeaderGrants reports whether the raw resolve payload carries a
// top-level browser_header_grants key. It is a presence pre-scan only; a
// payload that fails to parse counts as absent (strict parsing happens in the
// resolver).
func rawHasBrowserHeaderGrants(raw []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		return false
	}
	for key := range fields {
		if strings.EqualFold(key, "browser_header_grants") {
			return true
		}
	}

	return false
}

func validMatchSaltHex(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func echoRequestID(id string) string {
	if validRequestID(id) {
		return id
	}
	return ""
}

// handleDownload forwards the request to TaskAdder (tasks.Service), never rpc.DownloadEngine.
func (s *Server) handleDownload(req DownloadRequest) DownloadResponse {
	if s.taskAdder == nil {
		return DownloadResponse{Type: MsgTypeDownloadAck, Success: false, Error: "no task service"}
	}
	gid, err := s.taskAdder.AddUriFromExtension(req)
	if err != nil {
		return DownloadResponse{Type: MsgTypeDownloadAck, Success: false, Error: err.Error()}
	}
	return DownloadResponse{Type: MsgTypeDownloadAck, Success: true, GID: gid}
}
