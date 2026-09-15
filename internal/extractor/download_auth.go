package extractor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// The materialized "Authorization: Bearer <token>" line must fit the
	// aria2 header-line cap; the 22-byte prefix is reserved from the token
	// budget so a registered token can always materialize.
	downloadAuthTokenMaxBytes     = maxAria2HeaderLineBytes - len("Authorization: Bearer ")
	downloadAuthRegistryCapacity  = 256
	downloadAuthPendingPerInvoke  = 8
	downloadAuthEntryTTL          = 10 * time.Minute
	downloadAuthRefRandomHexBytes = 16 // minted as 32 lowercase hex chars
)

var (
	ErrDownloadAuthRegistryFull      = errors.New("download auth registry is full")
	ErrDownloadAuthInvocationLimit   = errors.New("download auth per-invocation registration limit reached")
	ErrDownloadAuthUnknownRef        = errors.New("download auth ref is unknown")
	ErrDownloadAuthNotMaterializable = errors.New("download auth ref is not usable")
)

type downloadAuthEntry struct {
	ref        string
	pack       VerifiedPackIdentity
	invocation uint64 // owning invocation; 0 == retained (post-EndInvocation)
	bound      bool
	boundHosts map[string]struct{}
	holders    map[string]struct{}
	token      []byte
	createdAt  time.Time
}

// DownloadAuthRegistry stores pack-minted bearer tokens behind opaque refs.
// Tokens never leave the host except as the materialized Authorization
// header; refs are invocation-scoped until bound and retained.
type DownloadAuthRegistry struct {
	mu      sync.Mutex
	entries map[string]*downloadAuthEntry
	now     func() time.Time
}

func NewDownloadAuthRegistry() *DownloadAuthRegistry {
	return &DownloadAuthRegistry{
		entries: make(map[string]*downloadAuthEntry),
		now:     time.Now,
	}
}

// Register stores token behind a fresh opaque ref owned by invocation.
// Callers perform the wire-level token checks before calling; Register
// re-verifies them so a host-internal caller cannot bypass the contract.
func (r *DownloadAuthRegistry) Register(invocation uint64, pack VerifiedPackIdentity, token []byte) (string, error) {
	if r == nil {
		return "", errors.New("download auth registry is not configured")
	}
	if err := validateDownloadAuthToken(token); err != nil {
		return "", err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	pending := 0
	for _, entry := range r.entries {
		if entry.invocation == invocation {
			pending++
		}
	}
	if pending >= downloadAuthPendingPerInvoke {
		return "", ErrDownloadAuthInvocationLimit
	}
	if len(r.entries) >= downloadAuthRegistryCapacity {
		return "", ErrDownloadAuthRegistryFull
	}

	ref, err := mintDownloadAuthRef()
	if err != nil {
		return "", err
	}
	r.entries[ref] = &downloadAuthEntry{
		ref:        ref,
		pack:       pack,
		invocation: invocation,
		boundHosts: make(map[string]struct{}),
		holders:    make(map[string]struct{}),
		token:      append([]byte(nil), token...),
		createdAt:  r.now(),
	}

	return ref, nil
}

// Bind marks a pending entry as referenced by an output item produced in the
// same invocation and records the item URL host for later materialization.
func (r *DownloadAuthRegistry) Bind(invocation uint64, ref string, pack VerifiedPackIdentity, host string) error {
	if r == nil {
		return errors.New("download auth registry is not configured")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	entry, ok := r.entries[ref]
	if !ok {
		return fmt.Errorf("%w: %s", ErrDownloadAuthUnknownRef, redactDownloadAuthRef(ref))
	}
	if entry.invocation == 0 || entry.invocation != invocation {
		return errors.New("download auth ref does not belong to this invocation")
	}
	if entry.pack != pack {
		return errors.New("download auth ref does not belong to this pack")
	}
	if host == "" {
		return errors.New("download auth bind requires a non-empty host")
	}
	entry.boundHosts[host] = struct{}{}
	entry.bound = true

	return nil
}

// EndInvocation transitions entries owned by invocation. Committed output
// retains bound entries (invocation cleared) and purges still-pending ones;
// uncommitted output purges everything.
func (r *DownloadAuthRegistry) EndInvocation(invocation uint64, committed bool) {
	if r == nil || invocation == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	for ref, entry := range r.entries {
		if entry.invocation != invocation {
			continue
		}
		if committed && entry.bound {
			entry.invocation = 0
			continue
		}
		r.deleteLocked(ref)
	}
}

// Claim records an outstanding holder for ref. It is idempotent per holder
// key; an empty ref is a no-op so callers can pass item.DownloadAuthRef
// unconditionally.
func (r *DownloadAuthRegistry) Claim(ref string, holderKey string) error {
	if r == nil || ref == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	entry, ok := r.entries[ref]
	if !ok {
		return fmt.Errorf("%w: %s", ErrDownloadAuthUnknownRef, redactDownloadAuthRef(ref))
	}
	entry.holders[holderKey] = struct{}{}

	return nil
}

// Release removes one holder. When the last holder leaves a retained entry,
// the entry (and its secret) is freed. An empty ref is a no-op.
func (r *DownloadAuthRegistry) Release(ref string, holderKey string) {
	if r == nil || ref == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	entry, ok := r.entries[ref]
	if !ok {
		return
	}
	if _, ok := entry.holders[holderKey]; !ok {
		return
	}
	delete(entry.holders, holderKey)
	if len(entry.holders) == 0 && entry.invocation == 0 {
		r.deleteLocked(ref)
	}
}

// Materialize returns a copy of the stored token for a fully-admitted ref.
// It never consumes the ref: the token is freed by holder release, expiry,
// or invalidation only.
func (r *DownloadAuthRegistry) Materialize(pack VerifiedPackIdentity, ref string, host string) (string, error) {
	if r == nil {
		return "", errors.New("download auth registry is not configured")
	}
	if ref == "" {
		return "", errors.New("download auth ref is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	entry, ok := r.entries[ref]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrDownloadAuthUnknownRef, redactDownloadAuthRef(ref))
	}
	if err := materializableDownloadAuthEntry(entry, pack, host); err != nil {
		return "", err
	}

	return string(append([]byte(nil), entry.token...)), nil
}

// ValidateRef applies the Materialize admission checks without revealing
// the token.
func (r *DownloadAuthRegistry) ValidateRef(pack VerifiedPackIdentity, ref string, host string) error {
	if r == nil {
		return errors.New("download auth registry is not configured")
	}
	if ref == "" {
		return errors.New("download auth ref is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	entry, ok := r.entries[ref]
	if !ok {
		return fmt.Errorf("%w: %s", ErrDownloadAuthUnknownRef, redactDownloadAuthRef(ref))
	}

	return materializableDownloadAuthEntry(entry, pack, host)
}

// Invalidate drops every entry and zeroes every secret.
func (r *DownloadAuthRegistry) Invalidate() {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for ref := range r.entries {
		r.deleteLocked(ref)
	}
}

// EntryCount reports live entries; for tests and diagnostics only.
func (r *DownloadAuthRegistry) EntryCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked(r.now())

	return len(r.entries)
}

func materializableDownloadAuthEntry(entry *downloadAuthEntry, pack VerifiedPackIdentity, host string) error {
	if entry.invocation != 0 {
		return fmt.Errorf("%w: ref is still invocation-scoped", ErrDownloadAuthNotMaterializable)
	}
	if !entry.bound {
		return fmt.Errorf("%w: ref was never bound to an output item", ErrDownloadAuthNotMaterializable)
	}
	if entry.pack != pack {
		return fmt.Errorf("%w: pack identity mismatch", ErrDownloadAuthNotMaterializable)
	}
	if _, ok := entry.boundHosts[host]; !ok {
		return fmt.Errorf("%w: host is not bound to this ref", ErrDownloadAuthNotMaterializable)
	}
	if len(entry.holders) == 0 {
		return fmt.Errorf("%w: ref has no outstanding holders", ErrDownloadAuthNotMaterializable)
	}

	return nil
}

func (r *DownloadAuthRegistry) sweepExpiredLocked(now time.Time) {
	for ref, entry := range r.entries {
		if now.Sub(entry.createdAt) >= downloadAuthEntryTTL {
			r.deleteLocked(ref)
		}
	}
}

func (r *DownloadAuthRegistry) deleteLocked(ref string) {
	entry, ok := r.entries[ref]
	if !ok {
		return
	}
	clear(entry.token)
	entry.token = nil
	delete(r.entries, ref)
}

func mintDownloadAuthRef() (string, error) {
	var raw [downloadAuthRefRandomHexBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint download auth ref: %w", err)
	}

	return downloadAuthRefPrefix + hex.EncodeToString(raw[:]), nil
}

// validateDownloadAuthToken re-checks the frozen wire constraints for the
// registered bearer token: 1..8192 bytes, valid UTF-8, no CR/LF, and never
// already prefixed with a "bearer " scheme (which would double-prefix the
// materialized header).
func validateDownloadAuthToken(token []byte) error {
	if len(token) == 0 || len(token) > downloadAuthTokenMaxBytes {
		return fmt.Errorf("token length must be between 1 and %d bytes", downloadAuthTokenMaxBytes)
	}
	if !utf8.Valid(token) {
		return errors.New("token must be valid utf-8")
	}
	for _, b := range token {
		if b == '\r' || b == '\n' {
			return errors.New("token must not contain CR/LF")
		}
	}
	if len(token) >= len("bearer ") && equalFoldASCII(string(token[:len("bearer ")]), "bearer ") {
		return errors.New("token must not include a bearer scheme prefix")
	}

	return nil
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}

	return true
}

// redactDownloadAuthRef shortens a ref for error text; the ref is opaque but
// trimming keeps error strings bounded without leaking the full value.
func redactDownloadAuthRef(ref string) string {
	const keep = 12
	if len(ref) <= keep {
		return ref
	}

	return ref[:keep] + "…"
}
