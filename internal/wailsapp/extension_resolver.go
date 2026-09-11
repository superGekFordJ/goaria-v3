//go:build extractor

package wailsapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
)

const (
	maxResolveCookies     = 64
	maxCookieNameBytes    = 256
	maxCookieValueBytes   = 4096
	maxCookieDomainBytes  = 253
	maxCookiePathBytes    = 1024
	maxSourceURLBytes     = 2048
	maxOptionalFieldBytes = 1024
	maxResolveSessions    = 16
	resolveSessionTTL     = 5 * time.Minute
	commitReceiptTTL      = 5 * time.Minute
)

type extensionResolveAdapter struct {
	dispatcher *extractor.AddTaskDispatcher

	mu            sync.Mutex
	epoch         uint64
	sessions      map[string]*leasedResolveSession
	receipts      map[string]commitReceipt
	flights       map[string]*resolveFlight
	extractCtx    context.Context
	extractCancel context.CancelFunc
}

type leasedResolveSession struct {
	epoch    uint64
	inserted time.Time
	lastUsed time.Time // LRU only; TTL is still measured from inserted
	items    map[string]extractor.ResolvedAddItem
}

type commitReceipt struct {
	digest string
	epoch  uint64
	stored time.Time
	result extension.CommitResult
}

type leaseRestoreToken struct {
	sessionID string
	inserted  time.Time
	epoch     uint64
}

const (
	receiptMiss = iota
	receiptHit
	receiptConflict
)

type resolveFlight struct {
	done       chan struct{}
	res        extractor.AddTaskResolution
	err        error
	lastStatus int
}

var errResolvePanicked = errors.New("extractor resolve failed")

func newExtensionResolveAdapter(d *extractor.AddTaskDispatcher) *extensionResolveAdapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &extensionResolveAdapter{
		dispatcher:    d,
		sessions:      make(map[string]*leasedResolveSession),
		receipts:      make(map[string]commitReceipt),
		flights:       make(map[string]*resolveFlight),
		extractCtx:    ctx,
		extractCancel: cancel,
	}
}

func (a *extensionResolveAdapter) Ready() bool {
	return a != nil && a.dispatcher != nil
}

// HeaderContextReady reports the full header-context chain is live: strict
// wire parse, request context, and broker enforcement all exist on this
// dispatcher.
func (a *extensionResolveAdapter) HeaderContextReady() bool {
	return a != nil && a.dispatcher != nil && a.dispatcher.HeaderContextCapable()
}

func (a *extensionResolveAdapter) Invalidate() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.epoch++
	a.sessions = make(map[string]*leasedResolveSession)
	a.receipts = make(map[string]commitReceipt)
	a.flights = make(map[string]*resolveFlight)
	if a.extractCancel != nil {
		a.extractCancel()
	}
	a.extractCtx, a.extractCancel = context.WithCancel(context.Background())
}

func (a *extensionResolveAdapter) RewriteCachedResolve(cached []byte) []byte {
	if a == nil || len(cached) == 0 {
		return cached
	}
	var ack extension.ExtractorResolveAck
	if err := json.Unmarshal(cached, &ack); err != nil {
		return cached
	}
	if ack.ErrorCode != "" || ack.Matched == nil || !*ack.Matched {
		return cached
	}
	if a.sessionLive(ack.SessionID) {
		return cached
	}
	rewritten, err := json.Marshal(extension.ExtractorResolveAck{
		Type:      ack.Type,
		RequestID: ack.RequestID,
		ErrorCode: extension.ErrCodeSessionExpired,
		Items:     []extension.ExtractorResolveAckItem{},
	})
	if err != nil {
		return cached
	}

	return rewritten
}

func (a *extensionResolveAdapter) HandleResolve(ctx context.Context, _ extension.RequestEnvelope, raw json.RawMessage) extension.ResolveResult {
	if a == nil || a.dispatcher == nil {
		return extension.ResolveResult{ErrorCode: extension.ErrCodeUnavailable}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	parent := a.extractCtx
	startEpoch := a.epoch
	a.mu.Unlock()
	if parent != nil {
		stop := context.AfterFunc(parent, cancel)
		defer stop()
	}

	input, errCode := parseExtractorResolveRequest(ctx, raw)
	if errCode != "" {
		return extension.ResolveResult{ErrorCode: errCode}
	}
	ctx = extractor.WithBrowserContext(ctx, input.Browser)

	resolution, lastStatus, err := a.resolveOnce(ctx, input)
	if ctx.Err() != nil {
		return mapResolveError(ctx.Err(), lastStatus)
	}
	if err != nil {
		return mapResolveError(err, lastStatus)
	}
	if !resolution.Matched {
		return extension.ResolveResult{Matched: false}
	}
	if len(resolution.Items) > extension.MaxResolveSessionItems {
		return extension.ResolveResult{ErrorCode: extension.ErrCodePackError}
	}

	return a.mintSession(resolution, startEpoch)
}

func (a *extensionResolveAdapter) resolveOnce(ctx context.Context, input parsedResolveInput) (res extractor.AddTaskResolution, lastStatus int, err error) {
	key := a.flightKey(input)
	a.mu.Lock()
	if existing, ok := a.flights[key]; ok {
		a.mu.Unlock()
		select {
		case <-existing.done:
			return existing.res, existing.lastStatus, existing.err
		case <-ctx.Done():
			select {
			case <-existing.done:
				return existing.res, existing.lastStatus, existing.err
			default:
				return extractor.AddTaskResolution{}, 0, ctx.Err()
			}
		}
	}
	flight := &resolveFlight{done: make(chan struct{})}
	a.flights[key] = flight
	a.mu.Unlock()

	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[Extension] extractor resolve panicked (%T)", rec)
			flight.res = extractor.AddTaskResolution{}
			flight.err = errResolvePanicked
			flight.lastStatus = 0
			res = flight.res
			err = flight.err
			lastStatus = 0
		}
		a.mu.Lock()
		if current, ok := a.flights[key]; ok && current == flight {
			delete(a.flights, key)
		}
		a.mu.Unlock()
		close(flight.done)
	}()

	res, err = a.dispatcher.Resolve(ctx, input.SourceURL)
	lastStatus = extractor.LastHTTPFetchStatus(ctx)
	flight.res = res
	flight.err = err
	flight.lastStatus = lastStatus

	return res, lastStatus, err
}

func (a *extensionResolveAdapter) mintSession(resolution extractor.AddTaskResolution, startEpoch uint64) extension.ResolveResult {
	sessionID, err := randomHandleID()
	if err != nil {
		return extension.ResolveResult{ErrorCode: extension.ErrCodeUnavailable}
	}
	leased := &leasedResolveSession{
		inserted: time.Now(),
		lastUsed: time.Now(),
		items:    make(map[string]extractor.ResolvedAddItem, len(resolution.Items)),
	}
	display := make([]extension.ResolveDisplayItem, 0, len(resolution.Items))
	var totalBytes int64
	for _, item := range resolution.Items {
		itemID, err := randomHandleID()
		if err != nil {
			return extension.ResolveResult{ErrorCode: extension.ErrCodeUnavailable}
		}
		leased.items[itemID] = extractor.CloneResolvedAddItem(item)
		display = append(display, extension.ResolveDisplayItem{
			ItemID:    itemID,
			Filename:  item.Filename,
			SizeBytes: item.SizeBytes,
			MimeType:  sanitizeAckMime(item.MimeType),
		})
		totalBytes += item.SizeBytes
	}
	a.mu.Lock()
	if a.epoch != startEpoch {
		a.mu.Unlock()
		return extension.ResolveResult{ErrorCode: extension.ErrCodeUnavailable}
	}
	leased.epoch = a.epoch
	a.insertSessionLocked(sessionID, leased)
	a.mu.Unlock()

	return extension.ResolveResult{
		Matched:    true,
		SessionID:  sessionID,
		TotalCount: len(display),
		TotalBytes: totalBytes,
		Items:      display,
	}
}

func (a *extensionResolveAdapter) insertSessionLocked(id string, session *leasedResolveSession) {
	now := time.Now()
	a.evictExpiredLocked(now)
	for len(a.sessions) >= maxResolveSessions {
		a.evictLRULocked()
	}
	a.sessions[id] = session
}

func (a *extensionResolveAdapter) evictExpiredLocked(now time.Time) {
	for id, session := range a.sessions {
		if now.Sub(session.inserted) >= resolveSessionTTL {
			delete(a.sessions, id)
		}
	}
	a.evictExpiredReceiptsLocked(now)
}

func (a *extensionResolveAdapter) evictExpiredReceiptsLocked(now time.Time) {
	for id, rec := range a.receipts {
		if rec.epoch != a.epoch || now.Sub(rec.stored) >= commitReceiptTTL {
			delete(a.receipts, id)
		}
	}
}

func (a *extensionResolveAdapter) evictLRULocked() {
	var oldestID string
	var oldest time.Time
	first := true
	for id, session := range a.sessions {
		if first || session.lastUsed.Before(oldest) {
			oldestID = id
			oldest = session.lastUsed
			first = false
		}
	}
	if oldestID != "" {
		delete(a.sessions, oldestID)
	}
}

func (a *extensionResolveAdapter) sessionLive(sessionID string) bool {
	if a == nil || sessionID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[sessionID]
	if !ok {
		return false
	}
	if session.epoch != a.epoch || time.Since(session.inserted) >= resolveSessionTTL {
		delete(a.sessions, sessionID)
		return false
	}
	session.lastUsed = time.Now()

	return true
}

func (a *extensionResolveAdapter) lookupLeasedItem(sessionID, itemID string) (extractor.ResolvedAddItem, bool) {
	if a == nil || sessionID == "" || itemID == "" {
		return extractor.ResolvedAddItem{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[sessionID]
	if !ok {
		return extractor.ResolvedAddItem{}, false
	}
	if session.epoch != a.epoch || time.Since(session.inserted) >= resolveSessionTTL {
		delete(a.sessions, sessionID)
		return extractor.ResolvedAddItem{}, false
	}
	item, ok := session.items[itemID]
	if !ok {
		return extractor.ResolvedAddItem{}, false
	}
	session.lastUsed = time.Now()

	return extractor.CloneResolvedAddItem(item), true
}

func (a *extensionResolveAdapter) consumeLeasedItems(sessionID string, itemIDs []string) (map[string]extractor.ResolvedAddItem, leaseRestoreToken, string) {
	if a == nil {
		return nil, leaseRestoreToken{}, extension.ErrCodeUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[sessionID]
	if !ok {
		return nil, leaseRestoreToken{}, extension.ErrCodeSessionExpired
	}
	if session.epoch != a.epoch || time.Since(session.inserted) >= resolveSessionTTL {
		delete(a.sessions, sessionID)
		return nil, leaseRestoreToken{}, extension.ErrCodeSessionExpired
	}
	for _, id := range itemIDs {
		if _, exists := session.items[id]; !exists {
			return nil, leaseRestoreToken{}, extension.ErrCodeInvalidRequest
		}
	}
	clones := make(map[string]extractor.ResolvedAddItem, len(itemIDs))
	for _, id := range itemIDs {
		clones[id] = extractor.CloneResolvedAddItem(session.items[id])
		delete(session.items, id)
	}
	session.lastUsed = time.Now()
	token := leaseRestoreToken{sessionID: sessionID, inserted: session.inserted, epoch: session.epoch}
	if len(session.items) == 0 {
		delete(a.sessions, sessionID)
	}

	return clones, token, ""
}

func (a *extensionResolveAdapter) restoreLeasedItems(token leaseRestoreToken, failedIDs []string, clones map[string]extractor.ResolvedAddItem) {
	if a == nil || token.sessionID == "" || len(failedIDs) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if token.epoch != a.epoch {
		return
	}
	session, ok := a.sessions[token.sessionID]
	if !ok {
		session = &leasedResolveSession{
			epoch:    token.epoch,
			inserted: token.inserted,
			lastUsed: time.Now(),
			items:    make(map[string]extractor.ResolvedAddItem, len(failedIDs)),
		}
		a.insertSessionLocked(token.sessionID, session)
		session = a.sessions[token.sessionID]
		if session == nil {
			return
		}
	}
	if session.epoch != token.epoch {
		return
	}
	for _, id := range failedIDs {
		item, exists := clones[id]
		if !exists {
			continue
		}
		session.items[id] = extractor.CloneResolvedAddItem(item)
	}
	session.lastUsed = time.Now()
}

func (a *extensionResolveAdapter) lookupReceipt(requestID, digest string) (extension.CommitResult, int) {
	if a == nil || requestID == "" {
		return extension.CommitResult{}, receiptMiss
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictExpiredReceiptsLocked(time.Now())
	rec, ok := a.receipts[requestID]
	if !ok {
		return extension.CommitResult{}, receiptMiss
	}
	if rec.epoch != a.epoch || time.Since(rec.stored) >= commitReceiptTTL {
		delete(a.receipts, requestID)
		return extension.CommitResult{}, receiptMiss
	}
	if rec.digest != digest {
		return extension.CommitResult{}, receiptConflict
	}

	return cloneCommitResult(rec.result), receiptHit
}

func (a *extensionResolveAdapter) storeReceipt(requestID, digest string, result extension.CommitResult, epoch uint64) {
	if a == nil || requestID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epoch != epoch {
		return
	}
	now := time.Now()
	a.evictExpiredReceiptsLocked(now)
	if a.receipts == nil {
		a.receipts = make(map[string]commitReceipt)
	}
	a.receipts[requestID] = commitReceipt{
		digest: digest,
		epoch:  epoch,
		stored: now,
		result: result,
	}
}

type parsedResolveInput struct {
	SourceURL string
	Browser   extractor.BrowserRequestContext
}

func (a *extensionResolveAdapter) flightKey(input parsedResolveInput) string {
	parts := []string{
		canonicalFlightSourceURL(input.SourceURL),
		cookieFingerprint(input.Browser.Cookies),
	}
	if fp := extractor.BrowserContextFingerprint(input.Browser); fp != "" {
		parts = append(parts, fp)
	}
	parts = append(parts, policyIdentity(a.dispatcher))

	return strings.Join(parts, "\n")
}

var browserHeaderGrantFieldKeys = []string{
	"source_origin", "target_url", "method",
	"captured_at_unix_ms", "expires_at_unix_ms", "headers",
}

var browserHeaderFieldKeys = []string{"name", "value"}

func parseExtractorResolveRequest(ctx context.Context, raw json.RawMessage) (parsedResolveInput, string) {
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(raw, &extra); err != nil {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	var grantRaw json.RawMessage
	grantKeys := 0
	grantKeyExact := false
	for key := range extra {
		if strings.EqualFold(key, "headers") || strings.EqualFold(key, "extra_headers") ||
			strings.EqualFold(key, "url") || strings.EqualFold(key, "final_url") {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		if strings.EqualFold(key, "browser_header_grants") {
			grantKeys++
			grantRaw = extra[key]
			if key == "browser_header_grants" {
				grantKeyExact = true
			}
		}
	}
	if grantKeys > 1 || (grantKeys == 1 && !grantKeyExact) {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	// Map decoding folds exact duplicate keys (last wins); the streaming pass
	// makes "at most one grant-bearing key" hold for exact duplicates too.
	if grantKeys == 1 && countTopLevelJSONKey(raw, "browser_header_grants") != 1 {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	if grantKeys == 1 && !extension.HeaderContextGranted(ctx) {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}

	var req extension.ExtractorResolveRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	// req.BrowserHeaderGrants is intentionally never read: grants only enter
	// through grantRaw + decodeBrowserHeaderGrantSpecs (key-set/duplicate-key
	// strictness the typed decoder cannot express).
	if req.SourceURL == "" || len(req.SourceURL) > maxSourceURLBytes || hasCRLF(req.SourceURL) {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	sourceURL := stripURLFragment(req.SourceURL)
	if _, ok := extractor.ParseHTTPURLHost(sourceURL); !ok {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	input := parsedResolveInput{SourceURL: sourceURL}
	srcOrigin, _ := canonicalOrigin(sourceURL)

	if hasControlBytes(req.UserAgent) || len(req.UserAgent) > maxOptionalFieldBytes {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	input.Browser.UserAgent = req.UserAgent
	if hasControlBytes(req.AcceptLanguage) || len(req.AcceptLanguage) > maxOptionalFieldBytes {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	input.Browser.AcceptLanguage = req.AcceptLanguage
	if req.Referer != "" {
		if hasControlBytes(req.Referer) || len(req.Referer) > maxOptionalFieldBytes {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		refOrigin, refOK := canonicalOrigin(req.Referer)
		if srcOrigin == "" || !refOK || srcOrigin != refOrigin {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		input.Browser.RefererOrigin = srcOrigin
	}
	if grantKeys == 1 {
		specs, ok := decodeBrowserHeaderGrantSpecs(grantRaw)
		if !ok {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		if len(specs) > 0 {
			grants, err := extractor.ValidateBrowserHeaderGrants(specs, srcOrigin, time.Now())
			if err != nil {
				return parsedResolveInput{}, extension.ErrCodeInvalidRequest
			}
			input.Browser.Grants = grants
		}
	}
	if len(req.Cookies) > maxResolveCookies {
		return parsedResolveInput{}, extension.ErrCodeInvalidRequest
	}
	cookies := make([]extractor.SessionCookie, 0, len(req.Cookies))
	for _, cookie := range req.Cookies {
		if cookie.HostOnly == nil || cookie.Secure == nil {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		if hasCRLF(cookie.Name) || hasCRLF(cookie.Value) || hasCRLF(cookie.Domain) || hasCRLF(cookie.Path) {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		if len(cookie.Name) > maxCookieNameBytes || len(cookie.Value) > maxCookieValueBytes ||
			len(cookie.Domain) > maxCookieDomainBytes || len(cookie.Path) > maxCookiePathBytes {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		if cookie.Name == "" {
			return parsedResolveInput{}, extension.ErrCodeInvalidRequest
		}
		if !extractor.ValidCookieName(cookie.Name) || !extractor.ValidCookieValue(cookie.Value) {
			continue
		}
		if !extractor.CookieDomainRelatedToSource(cookie.Domain, sourceURL) {
			continue
		}
		cookies = append(cookies, extractor.SessionCookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			Secure:   *cookie.Secure,
			HostOnly: *cookie.HostOnly,
		})
	}
	input.Browser.Cookies = cookies

	return input, ""
}

// decodeBrowserHeaderGrantSpecs strictly decodes the browser_header_grants
// array. Each grant must be an object with exactly the six wire keys and each
// header an object with exactly name/value; duplicate keys are rejected. An
// empty array decodes to nil (treated as absent by the caller).
func decodeBrowserHeaderGrantSpecs(raw json.RawMessage) ([]extractor.BrowserHeaderGrantSpec, bool) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	if len(items) == 0 {
		return nil, true
	}
	specs := make([]extractor.BrowserHeaderGrantSpec, 0, len(items))
	for _, item := range items {
		fields, ok := strictObjectFields(item, browserHeaderGrantFieldKeys)
		if !ok {
			return nil, false
		}
		var grant extension.BrowserHeaderGrant
		if err := json.Unmarshal(item, &grant); err != nil {
			return nil, false
		}
		var headerItems []json.RawMessage
		if err := json.Unmarshal(fields["headers"], &headerItems); err != nil {
			return nil, false
		}
		spec := extractor.BrowserHeaderGrantSpec{
			SourceOrigin:     grant.SourceOrigin,
			TargetURL:        grant.TargetURL,
			Method:           grant.Method,
			CapturedAtUnixMs: grant.CapturedAtUnixMs,
			ExpiresAtUnixMs:  grant.ExpiresAtUnixMs,
			Headers:          make([]extractor.BrowserHeaderSpec, 0, len(headerItems)),
		}
		for _, headerRaw := range headerItems {
			if _, ok := strictObjectFields(headerRaw, browserHeaderFieldKeys); !ok {
				return nil, false
			}
			var header extension.BrowserHeader
			if err := json.Unmarshal(headerRaw, &header); err != nil {
				return nil, false
			}
			spec.Headers = append(spec.Headers, extractor.BrowserHeaderSpec{Name: header.Name, Value: header.Value})
		}
		specs = append(specs, spec)
	}

	return specs, true
}

// strictObjectFields decodes one JSON object and requires its key set to be
// exactly want with no duplicate keys and no trailing data.
func strictObjectFields(raw json.RawMessage, want []string) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(want))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		if _, dup := fields[key]; dup {
			return nil, false
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	if len(fields) != len(want) {
		return nil, false
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			return nil, false
		}
	}

	return fields, true
}

// countTopLevelJSONKey streams the top-level object and counts exact
// occurrences of key. Map decoding folds duplicate keys (last wins), so a
// repeated key cannot be counted from the map alone.
func countTopLevelJSONKey(raw json.RawMessage, want string) int {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return -1
	}
	count := 0
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return -1
		}
		key, ok := keyToken.(string)
		if !ok {
			return -1
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return -1
		}
		if key == want {
			count++
		}
	}

	return count
}

func mapResolveError(err error, lastStatus int) extension.ResolveResult {
	if errors.Is(err, extractor.ErrResolveTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return extension.ResolveResult{ErrorCode: extension.ErrCodeTimeout}
	}
	if extractor.IsGenericAuthResolutionError(err) {
		if lastStatus == http.StatusUnauthorized {
			return extension.ResolveResult{ErrorCode: extension.ErrCodeAuthExpired}
		}

		return extension.ResolveResult{ErrorCode: extension.ErrCodePackError}
	}

	return extension.ResolveResult{ErrorCode: extension.ErrCodePackError}
}

func cookieFingerprint(cookies []extractor.SessionCookie) string {
	cloned := make([]extractor.SessionCookie, 0, len(cookies))
	for _, cookie := range cookies {
		cloned = append(cloned, extractor.CanonicalCookieFields(cookie))
	}
	sort.SliceStable(cloned, func(i, j int) bool {
		if cloned[i].Domain != cloned[j].Domain {
			return cloned[i].Domain < cloned[j].Domain
		}
		if cloned[i].Path != cloned[j].Path {
			return cloned[i].Path < cloned[j].Path
		}
		if cloned[i].Name != cloned[j].Name {
			return cloned[i].Name < cloned[j].Name
		}
		if cloned[i].HostOnly != cloned[j].HostOnly {
			return !cloned[i].HostOnly && cloned[j].HostOnly
		}

		return cloned[i].Value < cloned[j].Value
	})
	sum := sha256.New()
	for _, cookie := range cloned {
		fmt.Fprintf(sum, "%s\x00%s\x00%s\x00%s\x00%t\x00%t\n", cookie.Name, cookie.Value, cookie.Domain, cookie.Path, cookie.Secure, cookie.HostOnly)
	}

	return hex.EncodeToString(sum.Sum(nil))
}

func policyIdentity(d *extractor.AddTaskDispatcher) string {
	if d == nil || d.Registry() == nil {
		return ""
	}
	packs := d.Registry().Packs()
	hashes := make([]string, 0, len(packs))
	for _, pack := range packs {
		hashes = append(hashes, pack.Identity.PayloadSHA256)
	}
	sort.Strings(hashes)

	return strings.Join(hashes, ",")
}

func randomHandleID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(b[:]), nil
}

func canonicalFlightSourceURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(stripURLFragment(raw))
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return parsed.String()
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = host + ":" + port
	} else {
		parsed.Host = host
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)

	return parsed.String()
}

func stripURLFragment(raw string) string {
	if before, _, ok := strings.Cut(raw, "#"); ok {
		return before
	}

	return raw
}

// canonicalOrigin shares the extractor's grant source-origin canonicalizer so
// referer validation and grant source binding can never drift apart.
func canonicalOrigin(raw string) (string, bool) {
	return extractor.CanonicalBrowserGrantSourceOrigin(raw)
}

func hasCRLF(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

// hasControlBytes rejects C0 controls and DEL so typed fields can never reach
// the wire or produce ambiguous fingerprint separators.
func hasControlBytes(value string) bool {
	for i := range len(value) {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}

	return false
}

func sanitizeAckMime(mime string) string {
	mime = strings.TrimSpace(mime)
	if mime == "" || hasCRLF(mime) {
		return ""
	}
	slash := strings.IndexByte(mime, '/')
	if slash <= 0 || slash >= len(mime)-1 {
		return ""
	}
	typ, sub := mime[:slash], mime[slash+1:]
	if !isRFCToken(typ) || !isRFCToken(sub) {
		return ""
	}

	return typ + "/" + sub
}

func isRFCToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 32 || c >= 127 {
			return false
		}
		switch c {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=':
			return false
		}
	}

	return true
}
