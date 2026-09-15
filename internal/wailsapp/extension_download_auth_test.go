//go:build extractor

package wailsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"goaria-v3/internal/extractor"
)

func downloadAuthLeaseFixture(t *testing.T) (*extensionResolveAdapter, *extractor.DownloadAuthRegistry, extractor.VerifiedPackIdentity, string) {
	t.Helper()
	registry := extractor.NewDownloadAuthRegistry()
	dispatcher := extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{DownloadAuth: registry})
	adapter := newExtensionResolveAdapter(dispatcher)

	identity := extractor.VerifiedPackIdentity{
		PackID:          "xpk-alpha001",
		PackVersion:     "opaque-1",
		AssetSHA256:     strings.Repeat("1", 64),
		ManifestSHA256:  strings.Repeat("2", 64),
		PayloadSHA256:   strings.Repeat("3", 64),
		SignatureSHA256: strings.Repeat("4", 64),
		PublicKeySHA256: strings.Repeat("5", 64),
	}
	ref, err := registry.Register(77, identity, []byte("lease-token"))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Bind(77, ref, identity, "files.alpha.test"); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	registry.EndInvocation(77, true)

	return adapter, registry, identity, ref
}

func downloadAuthLeaseItem(identity extractor.VerifiedPackIdentity, ref string) extractor.ResolvedAddItem {
	return extractor.ResolvedAddItem{
		PackID:          identity.PackID,
		PackManifest:    extractor.Manifest{PackID: identity.PackID},
		PackIdentity:    identity,
		URL:             "https://files.alpha.test/file.bin",
		DownloadAuthRef: ref,
	}
}

func insertLeaseSession(t *testing.T, adapter *extensionResolveAdapter, sessionID string, items map[string]extractor.ResolvedAddItem) {
	t.Helper()
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if err := adapter.insertSessionLocked(sessionID, &leasedResolveSession{
		inserted: time.Now(),
		lastUsed: time.Now(),
		epoch:    adapter.epoch,
		items:    items,
	}); err != nil {
		t.Fatalf("insertSessionLocked() error = %v", err)
	}
}

func TestExtensionLeaseClaimsAndConsumesDownloadAuth(t *testing.T) {
	adapter, registry, identity, ref := downloadAuthLeaseFixture(t)
	item := downloadAuthLeaseItem(identity, ref)

	insertLeaseSession(t, adapter, "s1", map[string]extractor.ResolvedAddItem{"i1": item})
	if err := registry.ValidateRef(identity, ref, "files.alpha.test"); err != nil {
		t.Fatalf("ValidateRef() error = %v, want session claim", err)
	}

	clones, token, code := adapter.consumeLeasedItems("s1", []string{"i1"})
	if code != "" || len(clones) != 1 {
		t.Fatalf("consumeLeasedItems() code = %q clones = %d", code, len(clones))
	}
	if len(token.consumedClaims) != 1 {
		t.Fatalf("consumedClaims = %#v, want commit claim", token.consumedClaims)
	}
	// Claim-transfer: the commit holder keeps the ref materializable after
	// the session holder detached.
	if err := registry.ValidateRef(identity, ref, "files.alpha.test"); err != nil {
		t.Fatalf("ValidateRef() error = %v, want commit claim live", err)
	}

	adapter.releaseConsumedClaims(token)
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want freed after commit claims released", registry.EntryCount())
	}
}

func TestExtensionLeaseRestoreKeepsIndependentClaim(t *testing.T) {
	adapter, registry, identity, ref := downloadAuthLeaseFixture(t)
	item := downloadAuthLeaseItem(identity, ref)

	insertLeaseSession(t, adapter, "s1", map[string]extractor.ResolvedAddItem{"i1": item})
	clones, token, code := adapter.consumeLeasedItems("s1", []string{"i1"})
	if code != "" {
		t.Fatalf("consumeLeasedItems() code = %q", code)
	}
	adapter.releaseConsumedClaims(token)
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want freed before restore", registry.EntryCount())
	}

	// Restore after the ref died keeps the item in the session (restorable)
	// without resurrecting a holder: the next consume claims fresh and the
	// ref simply fails admission there.
	adapter.restoreLeasedItems(token, []string{"i1"}, clones)
	adapter.mu.Lock()
	session := adapter.sessions["s1"]
	adapter.mu.Unlock()
	if session == nil || session.items["i1"].DownloadAuthRef != ref {
		t.Fatalf("restored session = %#v, want item back", session)
	}
	if _, code := func() (map[string]extractor.ResolvedAddItem, string) {
		clones, token, code := adapter.consumeLeasedItems("s1", []string{"i1"})
		adapter.releaseConsumedClaims(token)
		return clones, code
	}(); code == "" {
		t.Fatal("re-consume after dead ref succeeded, want failure")
	}
}

func TestExtensionLeaseIndependentFlightClaims(t *testing.T) {
	adapter, registry, identity, ref := downloadAuthLeaseFixture(t)
	item := downloadAuthLeaseItem(identity, ref)

	// Two singleflight waiters each minted their own session over the same
	// ref; their claims are independent.
	insertLeaseSession(t, adapter, "s1", map[string]extractor.ResolvedAddItem{"i1": item})
	insertLeaseSession(t, adapter, "s2", map[string]extractor.ResolvedAddItem{"i1": item})

	clones1, token1, code1 := adapter.consumeLeasedItems("s1", []string{"i1"})
	if code1 != "" || len(clones1) != 1 {
		t.Fatalf("consume s1 code = %q", code1)
	}
	adapter.releaseConsumedClaims(token1)
	// s1 fully released, but s2's session claim keeps the ref alive.
	if err := registry.ValidateRef(identity, ref, "files.alpha.test"); err != nil {
		t.Fatalf("ValidateRef() error = %v, want s2 claim live", err)
	}
	clones2, token2, code2 := adapter.consumeLeasedItems("s2", []string{"i1"})
	if code2 != "" || len(clones2) != 1 {
		t.Fatalf("consume s2 code = %q", code2)
	}
	adapter.releaseConsumedClaims(token2)
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want all claims drained", registry.EntryCount())
	}
}

func TestExtensionLeaseInvalidateDropsClaims(t *testing.T) {
	adapter, registry, identity, ref := downloadAuthLeaseFixture(t)
	item := downloadAuthLeaseItem(identity, ref)
	insertLeaseSession(t, adapter, "s1", map[string]extractor.ResolvedAddItem{"i1": item})

	adapter.Invalidate()
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want invalidated registry drained", registry.EntryCount())
	}
	if err := registry.Claim(ref, "post-invalidate"); !errors.Is(err, extractor.ErrDownloadAuthUnknownRef) {
		t.Fatalf("Claim() error = %v, want stale ref rejection", err)
	}
}

func TestExtensionLeaseSessionExpiryReleasesClaim(t *testing.T) {
	adapter, registry, identity, ref := downloadAuthLeaseFixture(t)
	item := downloadAuthLeaseItem(identity, ref)

	adapter.mu.Lock()
	if err := adapter.insertSessionLocked("stale", &leasedResolveSession{
		inserted: time.Now().Add(-2 * resolveSessionTTL),
		lastUsed: time.Now().Add(-2 * resolveSessionTTL),
		epoch:    adapter.epoch,
		items:    map[string]extractor.ResolvedAddItem{"i1": item},
	}); err != nil {
		adapter.mu.Unlock()
		t.Fatalf("insertSessionLocked() error = %v", err)
	}
	adapter.mu.Unlock()

	// The stale session is evicted on the next insert, releasing its claim.
	insertLeaseSession(t, adapter, "fresh", map[string]extractor.ResolvedAddItem{})
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want expired session claim released", registry.EntryCount())
	}
}
