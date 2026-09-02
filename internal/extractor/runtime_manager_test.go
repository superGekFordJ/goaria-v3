package extractor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func makeTestCandidate(t *testing.T, packID, packVersion string, privKey ed25519.PrivateKey, pubKey ed25519.PublicKey, isZip bool, mutateManifest func(map[string]any)) RuntimePackCandidate {
	t.Helper()
	payload := validRunnerFixtureWASM()
	rawManifest := mustManifestJSON(t, payload, func(m map[string]any) {
		m["pack_id"] = packID
		m["pack_version"] = packVersion
		if mutateManifest != nil {
			mutateManifest(m)
		}
	})
	sig := ed25519.Sign(privKey, rawManifest)

	var zipBytes []byte
	assetPath := "sample.pack.zip"
	if isZip {
		zipBytes = []byte("zip content for " + packID)
	} else {
		assetPath = "sample.pack.zip" // lock pin
	}
	lockRaw := mustRuntimeLockJSON(t, packID, packVersion, assetPath, zipBytes, rawManifest, payload, sig, pubKey)

	manifest, err := decodeManifestStrict(rawManifest)
	if err != nil {
		t.Fatal(err)
	}

	return RuntimePackCandidate{
		VerifiedPack: VerifiedPack{
			Manifest: manifest,
			Payload:  cloneBytes(payload),
			Identity: VerifiedPackIdentity{
				PackID:          packID,
				PackVersion:     packVersion,
				AssetSHA256:     sha256HexString(zipBytes),
				ManifestSHA256:  sha256HexString(rawManifest),
				PayloadSHA256:   sha256HexString(payload),
				SignatureSHA256: sha256HexString(sig),
				PublicKeySHA256: sha256HexString(pubKey),
			},
		},
		ManifestJSON: rawManifest,
		Signature:    sig,
		LockJSON:     lockRaw,
		ZipBytes:     zipBytes,
	}
}

// Test Task 5: Recovery matrix and privacy
func TestRuntimeManagerOfflineRecovery(t *testing.T) {
	pubKey1, privKey1 := deterministicKeyPair(1)
	pubKey2, privKey2 := deterministicKeyPair(2)

	t.Run("missing index starts empty writable with revision 1", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, err := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		snap := mgr.CurrentSnapshot()
		if snap == nil || snap.Revision() != 1 {
			t.Fatalf("expected revision 1, got %v", snap)
		}
		if snap.Dispatcher() != nil || snap.TasksAdapter() != nil || snap.IngressDigests() != nil {
			t.Fatal("expected nil runtime accessors for zero packs")
		}
		if len(mgr.ListSources()) != 0 {
			t.Fatalf("expected 0 sources, got %d", len(mgr.ListSources()))
		}
		if len(mgr.RecoveryErrors()) != 0 {
			t.Fatalf("expected 0 recovery errors, got %d", len(mgr.RecoveryErrors()))
		}
	})

	t.Run("recovers healthy sources across all three kinds offline", func(t *testing.T) {
		tempDir := t.TempDir()
		store := newRuntimeStore(tempDir)

		c1 := makeTestCandidate(t, "xpk-zip", "1.0.0", privKey1, pubKey1, true, nil)
		c2 := makeTestCandidate(t, "xpk-dir", "1.0.0", privKey1, pubKey1, false, nil)
		c3 := makeTestCandidate(t, "xpk-remote", "1.0.0", privKey2, pubKey2, true, nil)

		// Persist candidates into store
		gen1 := strings.Repeat("1", 32)
		gen2 := strings.Repeat("2", 32)
		gen3 := strings.Repeat("3", 32)

		_, _ = store.writeCandidateToStaging(gen1, c1)
		_ = store.finalizeCandidateGeneration("xpk-zip", gen1)

		_, _ = store.writeCandidateToStaging(gen2, c2)
		_ = store.finalizeCandidateGeneration("xpk-dir", gen2)

		_, _ = store.writeCandidateToStaging(gen3, c3)
		_ = store.finalizeCandidateGeneration("xpk-remote", gen3)

		// Note: The original locators do NOT exist on disk or network!
		sources := []runtimeIndexSource{
			{
				SourceID:          strings.Repeat("a", 32),
				Kind:              RuntimeSourceKindLocalZip,
				Locator:           filepath.Join(tempDir, "does-not-exist.zip"),
				PackID:            "xpk-zip",
				PackVersion:       "1.0.0",
				SignerFingerprint: c1.VerifiedPack.Identity.PublicKeySHA256,
				CacheGeneration:   gen1,
			},
			{
				SourceID:          strings.Repeat("b", 32),
				Kind:              RuntimeSourceKindLocalDirectory,
				Locator:           filepath.Join(tempDir, "missing-dir"),
				PackID:            "xpk-dir",
				PackVersion:       "1.0.0",
				SignerFingerprint: c2.VerifiedPack.Identity.PublicKeySHA256,
				CacheGeneration:   gen2,
			},
			{
				SourceID:          strings.Repeat("c", 32),
				Kind:              RuntimeSourceKindRemoteLock,
				Locator:           "https://example.com/missing/remote.lock.json?secret=sensitive_query_123",
				PackID:            "xpk-remote",
				PackVersion:       "1.0.0",
				SignerFingerprint: c3.VerifiedPack.Identity.PublicKeySHA256,
				CacheGeneration:   gen3,
			},
		}
		_ = store.replaceIndex(sources)

		mgr, err := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})
		if err != nil {
			t.Fatalf("unexpected NewExtractorRuntimeManager: %v", err)
		}

		srcs := mgr.ListSources()
		for _, s := range srcs {
			if s.Status != RuntimeSourceStatusReady {
				t.Fatalf("expected ready status for %s, got %s (err: %s)", s.PackID, s.Status, s.ErrorCode)
			}
		}
		if len(srcs) != 3 {
			t.Fatalf("expected 3 sources, got %d", len(srcs))
		}

		snap := mgr.CurrentSnapshot()
		if snap == nil || snap.Revision() != 1 {
			t.Fatalf("expected revision 1, got %v", snap)
		}
		if snap.Dispatcher() == nil || snap.TasksAdapter() == nil || snap.IngressDigests() == nil {
			t.Fatal("expected active runtime components for non-empty snapshot")
		}
		for _, s := range srcs {
			if s.Status != RuntimeSourceStatusReady {
				t.Fatalf("expected ready status for %s, got %s (err: %s)", s.PackID, s.Status, s.ErrorCode)
			}
		}

		// Check Privacy redactions
		if srcs[0].DisplayName != "does-not-exist.zip" {
			t.Fatalf("expected local basename 'does-not-exist.zip', got %q", srcs[0].DisplayName)
		}
		if srcs[1].DisplayName != "missing-dir" {
			t.Fatalf("expected local basename 'missing-dir', got %q", srcs[1].DisplayName)
		}
		if srcs[2].DisplayName != "example.com" {
			t.Fatalf("expected remote display_name 'example.com', got %q", srcs[2].DisplayName)
		}

		// Ensure sensitive tokens (local parent dir, remote query/path) never leak in json
		jsonBytes, _ := json.Marshal(srcs)
		jsonStr := string(jsonBytes)
		for _, forbidden := range []string{"sensitive_query_123", tempDir, "missing/remote.lock.json"} {
			if strings.Contains(jsonStr, forbidden) {
				t.Fatalf("sensitive locator leaked in safe projection: %s", forbidden)
			}
		}
	})

	t.Run("one corrupted cache marks row unavailable and keeps healthy rows ready", func(t *testing.T) {
		tempDir := t.TempDir()
		store := newRuntimeStore(tempDir)

		c1 := makeTestCandidate(t, "xpk-healthy1", "1.0.0", privKey1, pubKey1, true, nil)
		c2 := makeTestCandidate(t, "xpk-corrupt", "1.0.0", privKey1, pubKey1, true, nil)
		c3 := makeTestCandidate(t, "xpk-healthy2", "1.0.0", privKey1, pubKey1, true, nil)

		gen1 := strings.Repeat("1", 32)
		gen2 := strings.Repeat("2", 32)
		gen3 := strings.Repeat("3", 32)

		_, _ = store.writeCandidateToStaging(gen1, c1)
		_ = store.finalizeCandidateGeneration("xpk-healthy1", gen1)

		_, _ = store.writeCandidateToStaging(gen2, c2)
		_ = store.finalizeCandidateGeneration("xpk-corrupt", gen2)

		_, _ = store.writeCandidateToStaging(gen3, c3)
		_ = store.finalizeCandidateGeneration("xpk-healthy2", gen3)

		// Corrupt c2 payload
		payloadPath := filepath.Join(store.generationDir("xpk-corrupt", gen2), "payload.wasm")
		_ = os.WriteFile(payloadPath, []byte("tampered!"), 0o600)

		sources := []runtimeIndexSource{
			{SourceID: strings.Repeat("a", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "1.zip"), PackID: "xpk-healthy1", PackVersion: "1.0.0", SignerFingerprint: c1.VerifiedPack.Identity.PublicKeySHA256, CacheGeneration: gen1},
			{SourceID: strings.Repeat("b", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "2.zip"), PackID: "xpk-corrupt", PackVersion: "1.0.0", SignerFingerprint: c2.VerifiedPack.Identity.PublicKeySHA256, CacheGeneration: gen2},
			{SourceID: strings.Repeat("c", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "3.zip"), PackID: "xpk-healthy2", PackVersion: "1.0.0", SignerFingerprint: c3.VerifiedPack.Identity.PublicKeySHA256, CacheGeneration: gen3},
		}
		_ = store.replaceIndex(sources)

		mgr, err := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}

		srcs := mgr.ListSources()
		if len(srcs) != 3 {
			t.Fatalf("expected 3 sources, got %d", len(srcs))
		}
		if srcs[0].Status != RuntimeSourceStatusReady || srcs[2].Status != RuntimeSourceStatusReady {
			t.Fatal("healthy rows should be ready")
		}
		if srcs[1].Status != RuntimeSourceStatusUnavailable || srcs[1].ErrorCode != string(RuntimePackLoadErrorHashMismatch) {
			t.Fatalf("corrupted row should be unavailable with hash_mismatch, got %s / %s", srcs[1].Status, srcs[1].ErrorCode)
		}

		// Registry in snapshot contains only the 2 healthy packs in order
		snapPacks := mgr.CurrentSnapshot().Registry().Packs()
		if len(snapPacks) != 2 || snapPacks[0].Identity.PackID != "xpk-healthy1" || snapPacks[1].Identity.PackID != "xpk-healthy2" {
			t.Fatalf("unexpected snapshot packs: %v", snapPacks)
		}
	})

	t.Run("embedded pack conflict marks recovered row unavailable", func(t *testing.T) {
		tempDir := t.TempDir()
		store := newRuntimeStore(tempDir)

		c1 := makeTestCandidate(t, "xpk-conflict", "1.0.0", privKey1, pubKey1, true, nil)
		gen1 := strings.Repeat("1", 32)
		_, _ = store.writeCandidateToStaging(gen1, c1)
		_ = store.finalizeCandidateGeneration("xpk-conflict", gen1)

		sources := []runtimeIndexSource{
			{SourceID: strings.Repeat("a", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "1.zip"), PackID: "xpk-conflict", PackVersion: "1.0.0", SignerFingerprint: c1.VerifiedPack.Identity.PublicKeySHA256, CacheGeneration: gen1},
		}
		_ = store.replaceIndex(sources)

		// Injected embedded pack has the same PackID
		embedded := EmbeddedPack{
			ManifestJSON: c1.ManifestJSON,
			Payload:      c1.VerifiedPack.Payload,
			Signature:    c1.Signature,
			AssetSHA256:  c1.VerifiedPack.Identity.AssetSHA256,
		}

		policy := DefaultTrustPolicy()
		policy.TrustedPublicKeys = []ed25519.PublicKey{pubKey1}

		mgr, err := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot:      tempDir,
			EmbeddedPacks: []EmbeddedPack{embedded},
			TrustPolicy:   policy,
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}

		srcs := mgr.ListSources()
		if len(srcs) != 1 || srcs[0].Status != RuntimeSourceStatusUnavailable || srcs[0].ErrorCode != string(RuntimeManagerErrorPackIDConflict) {
			t.Fatalf("expected pack_id_conflict for conflicting user row, got: %#v", srcs)
		}

		// Embedded pack is authoritative in registry
		snapPacks := mgr.CurrentSnapshot().Registry().Packs()
		if len(snapPacks) != 1 || snapPacks[0].Identity.PackID != "xpk-conflict" {
			t.Fatalf("embedded pack should be active in registry: %v", snapPacks)
		}
	})

	t.Run("malformed global index enters state_invalid and blocks mutations", func(t *testing.T) {
		tempDir := t.TempDir()
		store := newRuntimeStore(tempDir)
		_ = os.WriteFile(store.indexPath(), []byte(`{invalid json`), 0o600)

		mgr, err := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})
		if err != nil {
			t.Fatalf("unexpected constructor error: %v", err)
		}

		if errs := mgr.RecoveryErrors(); len(errs) != 1 || errs[0] != string(RuntimeManagerErrorStateInvalid) {
			t.Fatalf("expected state_invalid recovery error, got %v", errs)
		}

		// Mutations fail with state_invalid
		_, err = mgr.LoadSource(context.Background(), RuntimeSourceSpec{Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "1.zip")})
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorStateInvalid {
			t.Fatalf("expected state_invalid error on Load, got %v", err)
		}

		// sources.json on disk was NOT touched/overwritten
		content, _ := os.ReadFile(store.indexPath())
		if string(content) != `{invalid json` {
			t.Fatal("sources.json should not be overwritten on invalid state")
		}
	})
}

// Test Tasks 7 & 8: Mutations (Load, Reload, Remove)
func TestRuntimeManagerMutations(t *testing.T) {
	pubKey1, privKey1 := deterministicKeyPair(1)
	pubKey2, privKey2 := deterministicKeyPair(2)

	tempDir := t.TempDir()
	mgr, err := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
		DataRoot: tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	c1 := makeTestCandidate(t, "xpk-item1", "1.0.0", privKey1, pubKey1, true, nil)
	mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
		if spec.Locator == filepath.Join(tempDir, "pack1.zip") {
			return c1, nil
		}
		return RuntimePackCandidate{}, errors.New("unknown locator")
	}

	var state1 RuntimeSourceState
	t.Run("successful Load appends source and increments revision", func(t *testing.T) {
		var err error
		state1, err = mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "pack1.zip"),
		})
		if err != nil {
			t.Fatalf("LoadSource failed: %v", err)
		}
		if state1.PackID != "xpk-item1" || state1.Status != RuntimeSourceStatusReady {
			t.Fatalf("unexpected state: %#v", state1)
		}
		if mgr.CurrentSnapshot().Revision() != 2 {
			t.Fatalf("expected revision 2, got %d", mgr.CurrentSnapshot().Revision())
		}
		if len(mgr.ListSources()) != 1 {
			t.Fatalf("expected 1 source, got %d", len(mgr.ListSources()))
		}
	})

	t.Run("duplicate Load returns pack_id_conflict with no side effect", func(t *testing.T) {
		_, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "pack1.zip"),
		})
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorPackIDConflict {
			t.Fatalf("expected pack_id_conflict, got %v", err)
		}
		if mgr.CurrentSnapshot().Revision() != 2 {
			t.Fatal("revision should not increment on failed load")
		}
	})

	t.Run("reload rejects signer change and preserves old LKG", func(t *testing.T) {
		c1ChangedSigner := makeTestCandidate(t, "xpk-item1", "1.0.1", privKey2, pubKey2, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c1ChangedSigner, nil
		}

		_, err := mgr.ReloadSource(context.Background(), state1.SourceID)
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorSignerChanged {
			t.Fatalf("expected signer_changed error, got %v", err)
		}

		// Revision unchanged, source still ready on 1.0.0
		if mgr.CurrentSnapshot().Revision() != 2 {
			t.Fatal("revision should not increment on failed reload")
		}
		currentSources := mgr.ListSources()
		if currentSources[0].PackVersion != "1.0.0" {
			t.Fatalf("expected version 1.0.0 preserved, got %s", currentSources[0].PackVersion)
		}
	})

	t.Run("reload rejects pack id change and preserves old LKG", func(t *testing.T) {
		cDifferentID := makeTestCandidate(t, "xpk-changed-id", "1.0.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return cDifferentID, nil
		}

		_, err := mgr.ReloadSource(context.Background(), state1.SourceID)
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorPackIdentityChanged {
			t.Fatalf("expected pack_identity_changed, got %v", err)
		}
		if mgr.CurrentSnapshot().Revision() != 2 {
			t.Fatal("revision should not increment on failed reload")
		}
	})

	t.Run("successful reload increments revision and updates version", func(t *testing.T) {
		c1Updated := makeTestCandidate(t, "xpk-item1", "1.1.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c1Updated, nil
		}

		reloaded, err := mgr.ReloadSource(context.Background(), state1.SourceID)
		if err != nil {
			t.Fatalf("ReloadSource error: %v", err)
		}
		if reloaded.SourceID != state1.SourceID {
			t.Fatalf("source id changed: %s != %s", reloaded.SourceID, state1.SourceID)
		}
		if reloaded.PackVersion != "1.1.0" {
			t.Fatalf("expected version 1.1.0, got %s", reloaded.PackVersion)
		}
		if mgr.CurrentSnapshot().Revision() != 3 {
			t.Fatalf("expected revision 3, got %d", mgr.CurrentSnapshot().Revision())
		}
	})

	t.Run("remove source removes row and increments revision", func(t *testing.T) {
		removed, err := mgr.RemoveSource(context.Background(), state1.SourceID)
		if err != nil {
			t.Fatalf("RemoveSource error: %v", err)
		}
		if removed.SourceID != state1.SourceID {
			t.Fatalf("removed wrong source: %s", removed.SourceID)
		}
		if mgr.CurrentSnapshot().Revision() != 4 {
			t.Fatalf("expected revision 4, got %d", mgr.CurrentSnapshot().Revision())
		}
		if len(mgr.ListSources()) != 0 {
			t.Fatalf("expected 0 sources after remove, got %d", len(mgr.ListSources()))
		}
	})

	t.Run("remove unknown source returns invalid_source_id", func(t *testing.T) {
		_, err := mgr.RemoveSource(context.Background(), strings.Repeat("f", 32))
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorInvalidSourceID {
			t.Fatalf("expected invalid_source_id, got %v", err)
		}
		if mgr.CurrentSnapshot().Revision() != 4 {
			t.Fatal("revision should not increment on failed remove")
		}
	})
}

// Test Task 9: Deterministic interleaving and race detector coverage
func TestRuntimeManagerConcurrencyInterleavings(t *testing.T) {
	pubKey1, privKey1 := deterministicKeyPair(1)

	t.Run("concurrent Loads of same Pack ID yield exactly one winner", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		c := makeTestCandidate(t, "xpk-race", "1.0.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c, nil
		}

		const count = 8
		var startBarrier sync.WaitGroup
		startBarrier.Add(1)

		var done sync.WaitGroup
		var successes int32
		var conflicts int32

		for i := range count {
			done.Add(1)
			go func(id int) {
				defer done.Done()
				startBarrier.Wait()

				_, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
					Kind:    RuntimeSourceKindLocalZip,
					Locator: filepath.Join(tempDir, fmt.Sprintf("%d.zip", id)),
				})
				if err == nil {
					atomic.AddInt32(&successes, 1)
				} else {
					var mgrErr *RuntimeManagerError
					if errors.As(err, &mgrErr) && mgrErr.Code == RuntimeManagerErrorPackIDConflict {
						atomic.AddInt32(&conflicts, 1)
					}
				}
			}(i)
		}

		startBarrier.Done()
		done.Wait()

		if successes != 1 {
			t.Fatalf("expected exactly 1 winner, got %d", successes)
		}
		if conflicts != count-1 {
			t.Fatalf("expected %d conflicts, got %d", count-1, conflicts)
		}
		if len(mgr.ListSources()) != 1 {
			t.Fatalf("expected 1 source, got %d", len(mgr.ListSources()))
		}
	})

	t.Run("concurrent Reloads of same source yield one winner and one stale concurrent_change", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		c := makeTestCandidate(t, "xpk-reload-race", "1.0.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c, nil
		}

		loaded, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "base.zip"),
		})
		if err != nil {
			t.Fatal(err)
		}

		cNew := makeTestCandidate(t, "xpk-reload-race", "1.1.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return cNew, nil
		}

		var startBarrier sync.WaitGroup
		startBarrier.Add(1)

		var done sync.WaitGroup
		var winnerCount int32
		var staleCount int32

		for range 2 {
			done.Go(func() {
				startBarrier.Wait()

				_, err := mgr.ReloadSource(context.Background(), loaded.SourceID)
				if err == nil {
					atomic.AddInt32(&winnerCount, 1)
				} else {
					var mgrErr *RuntimeManagerError
					if errors.As(err, &mgrErr) && mgrErr.Code == RuntimeManagerErrorConcurrentChange {
						atomic.AddInt32(&staleCount, 1)
					}
				}
			})
		}

		startBarrier.Done()
		done.Wait()

		if winnerCount != 1 || staleCount != 1 {
			t.Fatalf("expected 1 winner and 1 stale, got winners=%d, stales=%d", winnerCount, staleCount)
		}
	})

	t.Run("readers never block on writeMu during slow disk operations", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		c := makeTestCandidate(t, "xpk-unblocked", "1.0.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c, nil
		}

		// Lock writeMu to simulate an in-progress commit
		mgr.writeMu.Lock()

		// Read methods should execute immediately without blocking
		doneCh := make(chan struct{})
		go func() {
			_ = mgr.CurrentSnapshot()
			_ = mgr.ListSources()
			_ = mgr.RecoveryErrors()
			close(doneCh)
		}()

		select {
		case <-doneCh:
			// Success: readers did not block on writeMu!
		case <-context.Background().Done():
		}

		mgr.writeMu.Unlock()
	})

	t.Run("Remove cleanup racing new Load of same Pack ID deletes only old generation", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		c := makeTestCandidate(t, "xpk-reused", "1.0.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c, nil
		}

		loaded, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "first.zip"),
		})
		if err != nil {
			t.Fatal(err)
		}

		// Remove the source
		_, err = mgr.RemoveSource(context.Background(), loaded.SourceID)
		if err != nil {
			t.Fatal(err)
		}

		// Immediately load a new pack with the same Pack ID
		loaded2, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "second.zip"),
		})
		if err != nil {
			t.Fatalf("LoadSource of reused pack id failed: %v", err)
		}

		// Verify the new generation survives
		store := newRuntimeStore(tempDir)
		currSources := mgr.ListSources()
		if len(currSources) != 1 || currSources[0].SourceID != loaded2.SourceID {
			t.Fatalf("unexpected sources: %#v", currSources)
		}

		// Read cached candidate of the new generation - must still be intact!
		rec := mgr.current.Load().sources[0]
		recovered, err := store.readCachedCandidate(context.Background(), "xpk-reused", rec.cacheGeneration, RuntimeSourceKindLocalZip)
		if err != nil {
			t.Fatalf("new generation was improperly deleted by old cleanup: %v", err)
		}
		if recovered.VerifiedPack.Manifest.PackID != "xpk-reused" {
			t.Fatalf("unexpected pack id: %s", recovered.VerifiedPack.Manifest.PackID)
		}
	})
}

func TestRuntimeManagerCornerCases(t *testing.T) {
	pubKey1, privKey1 := deterministicKeyPair(1)

	t.Run("source limit 128 blocks 129th Load", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		// Artificially populate 128 sources in current state
		var sources []internalSourceRecord
		for i := range 128 {
			sources = append(sources, internalSourceRecord{
				sourceID:          fmt.Sprintf("%032x", i),
				kind:              RuntimeSourceKindLocalZip,
				locator:           filepath.Join(tempDir, fmt.Sprintf("%d.zip", i)),
				packID:            fmt.Sprintf("xpk-test-%d", i),
				packVersion:       "1.0.0",
				signerFingerprint: strings.Repeat("f", 64),
				cacheGeneration:   fmt.Sprintf("%032x", i),
				status:            RuntimeSourceStatusReady,
			})
		}
		mgr.current.Store(&managerState{
			snapshot:      mgr.CurrentSnapshot(),
			sources:       sources,
			stateWritable: true,
		})

		// 129th Load should fail before calling loader
		loaderCalled := false
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			loaderCalled = true
			return RuntimePackCandidate{}, nil
		}

		_, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "129.zip"),
		})
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorSourceLimitReached {
			t.Fatalf("expected source_limit_reached, got %v", err)
		}
		if loaderCalled {
			t.Fatal("loader should not be called when limit reached")
		}
	})

	t.Run("cancellation returns cancelled without side effect", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := mgr.LoadSource(ctx, RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "cancelled.zip"),
		})
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorCancelled {
			t.Fatalf("expected cancelled error, got %v", err)
		}
		if mgr.CurrentSnapshot().Revision() != 1 {
			t.Fatal("revision should not increment on cancellation")
		}
	})

	t.Run("alias and auth dependency failures fail closed", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		// Alias pack candidate without HostPolicyResolver
		aliasCand := makeTestCandidate(t, "xpk-alias", "1.0.0", privKey1, pubKey1, true, func(m map[string]any) {
			m["domains"] = []map[string]any{}
			m["domain_policy_refs"] = []string{"dpr-test"}
			m["broker_policy_refs"] = []string{"bpr-test"}
		})
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return aliasCand, nil
		}

		_, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "alias.zip"),
		})
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorPolicyUnavailable {
			t.Fatalf("expected policy_unavailable, got %v", err)
		}

		// Auth pack candidate without HostAuthRuntime
		authCand := makeTestCandidate(t, "xpk-auth", "1.0.0", privKey1, pubKey1, true, func(m map[string]any) {
			m["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityAuthProfile)}
		})
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return authCand, nil
		}

		_, err = mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "auth.zip"),
		})
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorAuthRuntimeUnavailable {
			t.Fatalf("expected auth_runtime_unavailable, got %v", err)
		}
	})

	t.Run("remove unavailable row clears ownership and preserves other rows", func(t *testing.T) {
		tempDir := t.TempDir()
		store := newRuntimeStore(tempDir)

		c1 := makeTestCandidate(t, "xpk-ok1", "1.0.0", privKey1, pubKey1, true, nil)
		c3 := makeTestCandidate(t, "xpk-ok3", "1.0.0", privKey1, pubKey1, true, nil)
		gen1 := strings.Repeat("1", 32)
		gen2 := strings.Repeat("2", 32)
		gen3 := strings.Repeat("3", 32)

		_, _ = store.writeCandidateToStaging(gen1, c1)
		_ = store.finalizeCandidateGeneration("xpk-ok1", gen1)
		_, _ = store.writeCandidateToStaging(gen3, c3)
		_ = store.finalizeCandidateGeneration("xpk-ok3", gen3)

		// gen2 is missing -> row b is unavailable
		sources := []runtimeIndexSource{
			{SourceID: strings.Repeat("a", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "1.zip"), PackID: "xpk-ok1", PackVersion: "1.0.0", SignerFingerprint: c1.VerifiedPack.Identity.PublicKeySHA256, CacheGeneration: gen1},
			{SourceID: strings.Repeat("b", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "2.zip"), PackID: "xpk-missing", PackVersion: "1.0.0", SignerFingerprint: strings.Repeat("f", 64), CacheGeneration: gen2},
			{SourceID: strings.Repeat("c", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "3.zip"), PackID: "xpk-ok3", PackVersion: "1.0.0", SignerFingerprint: c3.VerifiedPack.Identity.PublicKeySHA256, CacheGeneration: gen3},
		}
		_ = store.replaceIndex(sources)

		mgr, err := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})
		if err != nil {
			t.Fatal(err)
		}

		srcs := mgr.ListSources()
		if len(srcs) != 3 || srcs[1].Status != RuntimeSourceStatusUnavailable {
			t.Fatalf("expected row 1 unavailable: %#v", srcs)
		}

		// Remove the unavailable row
		removed, err := mgr.RemoveSource(context.Background(), strings.Repeat("b", 32))
		if err != nil {
			t.Fatalf("failed to remove unavailable row: %v", err)
		}
		if removed.PackID != "xpk-missing" {
			t.Fatalf("unexpected removed pack: %s", removed.PackID)
		}

		// Remaining sources are healthy, preserving order
		remSrcs := mgr.ListSources()
		if len(remSrcs) != 2 || remSrcs[0].PackID != "xpk-ok1" || remSrcs[1].PackID != "xpk-ok3" {
			t.Fatalf("unexpected remaining sources: %#v", remSrcs)
		}

		// Snapshot packs are still both healthy packs
		snapPacks := mgr.CurrentSnapshot().Registry().Packs()
		if len(snapPacks) != 2 || snapPacks[0].Identity.PackID != "xpk-ok1" || snapPacks[1].Identity.PackID != "xpk-ok3" {
			t.Fatalf("unexpected snap packs: %#v", snapPacks)
		}
	})

	t.Run("safeDisplayName trims unicode runes without splitting multi-byte chars", func(t *testing.T) {
		tempDir := t.TempDir()
		longChineseName := strings.Repeat("中", 70) + ".zip"
		res := safeDisplayName(RuntimeSourceKindLocalZip, filepath.Join(tempDir, longChineseName))
		runes := []rune(res)
		if len(runes) != 64 {
			t.Fatalf("expected 64 runes, got %d", len(runes))
		}
		if !strings.HasPrefix(res, strings.Repeat("中", 64)) {
			t.Fatalf("unexpected trimmed string: %s", res)
		}
	})

	t.Run("reload rejects conflict with embedded pack", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		c := makeTestCandidate(t, "xpk-reloaded", "1.0.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c, nil
		}

		loaded, err := mgr.LoadSource(context.Background(), RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "reload.zip"),
		})
		if err != nil {
			t.Fatal(err)
		}

		// Inject embedded pack with same pack ID
		mgr.embeddedPacks = append(mgr.embeddedPacks, c.VerifiedPack)

		_, err = mgr.ReloadSource(context.Background(), loaded.SourceID)
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorPackIDConflict {
			t.Fatalf("expected pack_id_conflict, got %v", err)
		}
	})

	t.Run("remove source supports cancellation", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := mgr.RemoveSource(ctx, strings.Repeat("a", 32))
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorCancelled {
			t.Fatalf("expected cancelled error, got %v", err)
		}
	})

	t.Run("cold start recovery respects context cancellation", func(t *testing.T) {
		tempDir := t.TempDir()
		store := newRuntimeStore(tempDir)

		c1 := makeTestCandidate(t, "xpk-cancel", "1.0.0", privKey1, pubKey1, true, nil)
		gen1 := strings.Repeat("1", 32)
		_, _ = store.writeCandidateToStaging(gen1, c1)
		_ = store.finalizeCandidateGeneration("xpk-cancel", gen1)

		sources := []runtimeIndexSource{
			{SourceID: strings.Repeat("a", 32), Kind: RuntimeSourceKindLocalZip, Locator: filepath.Join(tempDir, "1.zip"), PackID: "xpk-cancel", PackVersion: "1.0.0", SignerFingerprint: c1.VerifiedPack.Identity.PublicKeySHA256, CacheGeneration: gen1},
		}
		_ = store.replaceIndex(sources)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := NewExtractorRuntimeManager(ctx, ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorCancelled {
			t.Fatalf("expected cancelled error during recovery, got %v", err)
		}
	})

	t.Run("pre-commit cancellation inside lock returns cancelled", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr, _ := NewExtractorRuntimeManager(context.Background(), ExtractorRuntimeManagerConfig{
			DataRoot: tempDir,
		})

		c := makeTestCandidate(t, "xpk-precommit", "1.0.0", privKey1, pubKey1, true, nil)
		mgr.testLoaderOverride = func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
			return c, nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		mgr.testPreCommitHook = func() {
			cancel()
		}

		_, err := mgr.LoadSource(ctx, RuntimeSourceSpec{
			Kind:    RuntimeSourceKindLocalZip,
			Locator: filepath.Join(tempDir, "precommit.zip"),
		})
		var mgrErr *RuntimeManagerError
		if !errors.As(err, &mgrErr) || mgrErr.Code != RuntimeManagerErrorCancelled {
			t.Fatalf("expected cancelled error, got %v", err)
		}
	})
}
