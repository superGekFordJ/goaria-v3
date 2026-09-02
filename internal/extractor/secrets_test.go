package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthProfileStoreSetSnapshotAndResolveRedacted(t *testing.T) {
	store := newTempAuthProfileStore(t)
	secret := "raw-bearer-token"

	snapshot, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:          "xpk-fixture01",
		ProfileID:       "default",
		Kind:            AuthSecretKindBearer,
		Secret:          secret,
		AllowedDomains:  []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
		RedactedDisplay: "Bearer ra…en",
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}
	assertSnapshotRedacted(t, snapshot, secret)
	if !snapshot.HasSecret || snapshot.Kind != AuthSecretKindBearer || snapshot.PackID != "xpk-fixture01" || snapshot.ProfileID != "default" {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	snapshots, err := store.AuthProfileSnapshots(context.Background(), "xpk-fixture01")
	if err != nil {
		t.Fatalf("AuthProfileSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("AuthProfileSnapshots() = %d, want 1", len(snapshots))
	}
	assertSnapshotRedacted(t, snapshots[0], secret)

	resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "default", "https://api.fixture.invalid/path")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderName != "Authorization" || resolved.HeaderValue != "Bearer "+secret {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestAuthProfileStorePersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	secret := "persisted-token"
	store, err := NewFileAuthProfileStore(path)
	if err != nil {
		t.Fatalf("NewFileAuthProfileStore() error = %v", err)
	}

	_, err = store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindBearer,
		Secret:         secret,
		AllowedDomains: []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat secret file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("secret file permissions = %v, want owner-only", info.Mode().Perm())
	}

	reloaded, err := NewFileAuthProfileStore(path)
	if err != nil {
		t.Fatalf("NewFileAuthProfileStore(reload) error = %v", err)
	}
	resolved, err := reloaded.ResolveAuthProfile(context.Background(), "xpk-fixture01", "default", "https://fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderValue != "Bearer "+secret {
		t.Fatalf("HeaderValue = %q, want persisted token", resolved.HeaderValue)
	}
}

func TestAuthProfileStoreSetIsAtomicWhenPersistenceFails(t *testing.T) {
	store := newTempAuthProfileStore(t)
	_, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindBearer,
		Secret:         "old-token",
		AllowedDomains: []DomainRule{{Host: "fixture.invalid"}},
	})
	if err != nil {
		t.Fatalf("initial SetAuthProfile() error = %v", err)
	}

	store.mu.Lock()
	store.persistFn = func(map[string]authProfileRecord) error {
		return errors.New("forced persist failure with Authorization: Bearer new-token")
	}
	store.mu.Unlock()

	_, err = store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindBearer,
		Secret:         "new-token",
		AllowedDomains: []DomainRule{{Host: "fixture.invalid"}},
	})
	if err == nil {
		t.Fatal("SetAuthProfile() error = nil, want forced persistence failure")
	}
	if strings.Contains(err.Error(), "new-token") {
		t.Fatalf("SetAuthProfile() leaked new token in error: %v", err)
	}

	resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "default", "https://fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderValue != "Bearer old-token" {
		t.Fatalf("HeaderValue = %q, want old token after failed update", resolved.HeaderValue)
	}
}

func TestAuthProfileStoreRejectsInvalidInputs(t *testing.T) {
	valid := AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindBearer,
		Secret:         "token",
		AllowedDomains: []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
	}

	tests := []struct {
		name   string
		mutate func(*AuthProfileUpdate)
	}{
		{name: "invalid pack id", mutate: func(update *AuthProfileUpdate) { update.PackID = "FixturePack" }},
		{name: "invalid profile id", mutate: func(update *AuthProfileUpdate) { update.ProfileID = "Default" }},
		{name: "empty token", mutate: func(update *AuthProfileUpdate) { update.Secret = "" }},
		{name: "unsupported kind", mutate: func(update *AuthProfileUpdate) { update.Kind = AuthSecretKind("basic") }},
		{name: "invalid domain", mutate: func(update *AuthProfileUpdate) { update.AllowedDomains = []DomainRule{{Host: "evil..test"}} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTempAuthProfileStore(t)
			update := valid
			tt.mutate(&update)

			_, err := store.SetAuthProfile(context.Background(), update)
			if err == nil {
				t.Fatal("SetAuthProfile() error = nil, want error")
			}
			if strings.Contains(err.Error(), valid.Secret) {
				t.Fatalf("SetAuthProfile() leaked token: %v", err)
			}
		})
	}

	store := newTempAuthProfileStore(t)
	_, err := store.SetAuthProfile(context.Background(), valid)
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "default", "https://evil.test/path"); err == nil {
		t.Fatal("ResolveAuthProfile() error = nil for denied domain, want error")
	}
}

func TestAuthProfileStoreRejectsCRLFAndExpiredDeniedProfilesWithoutLeaks(t *testing.T) {
	for _, secret := range []string{"token\nwith-newline", "token\rwith-carriage-return"} {
		t.Run("crlf", func(t *testing.T) {
			store := newTempAuthProfileStore(t)
			_, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
				PackID:         "xpk-fixture01",
				ProfileID:      "default",
				Kind:           AuthSecretKindBearer,
				Secret:         secret,
				AllowedDomains: []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
			})
			if err == nil {
				t.Fatal("SetAuthProfile() error = nil, want CR/LF rejection")
			}
			for _, forbidden := range []string{secret, "token", "with-newline", "with-carriage-return"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("SetAuthProfile() leaked %q: %v", forbidden, err)
				}
			}
			snapshots, err := store.AuthProfileSnapshots(context.Background(), "xpk-fixture01")
			if err != nil {
				t.Fatalf("AuthProfileSnapshots() error = %v", err)
			}
			if len(snapshots) != 0 {
				t.Fatalf("AuthProfileSnapshots() = %#v, want no persisted CR/LF profile", snapshots)
			}
		})
	}

	expiredSecret := "expired-store-secret"
	expiresAt := time.Now().Add(-time.Minute)
	store := newTempAuthProfileStore(t)
	if _, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindBearer,
		Secret:         expiredSecret,
		AllowedDomains: []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
		ExpiresAt:      &expiresAt,
	}); err != nil {
		t.Fatalf("SetAuthProfile() expired profile error = %v", err)
	}
	_, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "default", "https://api.fixture.invalid/path")
	if err == nil {
		t.Fatal("ResolveAuthProfile() error = nil for expired profile")
	}
	if strings.Contains(err.Error(), expiredSecret) {
		t.Fatalf("ResolveAuthProfile() leaked expired secret: %v", err)
	}

	deniedSecret := "denied-domain-secret"
	store = newTempAuthProfileStore(t)
	if _, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindBearer,
		Secret:         deniedSecret,
		AllowedDomains: []DomainRule{{Host: "fixture.invalid"}},
	}); err != nil {
		t.Fatalf("SetAuthProfile() denied-domain profile error = %v", err)
	}
	_, err = store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "default", "https://api.fixture.invalid/path?token=query-secret")
	if err == nil {
		t.Fatal("ResolveAuthProfile() error = nil for denied subdomain")
	}
	for _, forbidden := range []string{deniedSecret, "query-secret"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("ResolveAuthProfile() leaked %q: %v", forbidden, err)
		}
	}
}

func TestAuthProfileStoreClearRemovesToken(t *testing.T) {
	store := newTempAuthProfileStore(t)
	_, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindBearer,
		Secret:         "clear-token",
		AllowedDomains: []DomainRule{{Host: "fixture.invalid"}},
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}

	if err := store.ClearAuthProfile(context.Background(), "xpk-fixture01", "default"); err != nil {
		t.Fatalf("ClearAuthProfile() error = %v", err)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "default", "https://fixture.invalid/d/abc"); err == nil {
		t.Fatal("ResolveAuthProfile() error = nil after clear, want error")
	}
	snapshots, err := store.AuthProfileSnapshots(context.Background(), "xpk-fixture01")
	if err != nil {
		t.Fatalf("AuthProfileSnapshots() error = %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("AuthProfileSnapshots() = %d, want 0 after clear", len(snapshots))
	}
}

func TestAuthProfileStoreErrorsAndSnapshotsDoNotExposeSecrets(t *testing.T) {
	store := newTempAuthProfileStore(t)
	secret := "super-secret-token"
	cookie := "sid=raw-cookie-value"

	snapshot, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "default",
		Kind:           AuthSecretKindCookie,
		Secret:         cookie,
		AllowedDomains: []DomainRule{{Host: "fixture.invalid"}},
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}
	assertSnapshotRedacted(t, snapshot, cookie)

	_, err = store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         "xpk-fixture01",
		ProfileID:      "other",
		Kind:           AuthSecretKindBearer,
		Secret:         secret,
		AllowedDomains: []DomainRule{{Host: "fixture.invalid"}},
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}
	_, err = store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "other", "https://evil.test/?token="+secret)
	if err == nil {
		t.Fatal("ResolveAuthProfile() error = nil, want denied domain")
	}
	for _, forbidden := range []string{secret, cookie, "raw-cookie-value"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

func TestAuthProfileStoreSanitizesCallerControlledRedactedDisplay(t *testing.T) {
	store := newTempAuthProfileStore(t)
	bearerSnapshot, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:          "xpk-fixture01",
		ProfileID:       "bearer-display",
		Kind:            AuthSecretKindBearer,
		Secret:          "Bearer stripped-token-secret",
		AllowedDomains:  []DomainRule{{Host: "fixture.invalid"}},
		RedactedDisplay: "safe? stripped-token-secret",
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() bearer error = %v", err)
	}
	if strings.Contains(bearerSnapshot.RedactedDisplay, "stripped-token-secret") || strings.Contains(bearerSnapshot.RedactedDisplay, "Bearer stripped-token-secret") {
		t.Fatalf("bearer RedactedDisplay leaked derived token: %#v", bearerSnapshot)
	}

	cookieSnapshot, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:          "xpk-fixture01",
		ProfileID:       "cookie-display",
		Kind:            AuthSecretKindCookie,
		Secret:          "sid=cookie-secret; refresh=second-secret",
		AllowedDomains:  []DomainRule{{Host: "fixture.invalid"}},
		RedactedDisplay: "sid cookie-secret refresh=second-secret",
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() cookie error = %v", err)
	}
	for _, forbidden := range []string{"sid=cookie-secret", "cookie-secret", "refresh=second-secret", "second-secret"} {
		if strings.Contains(cookieSnapshot.RedactedDisplay, forbidden) {
			t.Fatalf("cookie RedactedDisplay leaked %q: %#v", forbidden, cookieSnapshot)
		}
	}
}

func TestAuthProfileStoreSanitizesLoadedRedactedDisplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	updatedAt := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := updatedAt.Add(time.Hour)
	disk := authProfileDiskState{Profiles: []authProfileRecord{
		{
			PackID:          "xpk-fixture01",
			ProfileID:       "default",
			Kind:            AuthSecretKindBearer,
			Secret:          "loaded-secret-token",
			AllowedDomains:  []DomainRule{{Host: "fixture.invalid"}},
			UpdatedAt:       updatedAt,
			ExpiresAt:       &expiresAt,
			RedactedDisplay: "legacy loaded-secret-token",
		},
	}}
	bytes, err := json.Marshal(disk)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, bytes, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewFileAuthProfileStore(path)
	if err != nil {
		t.Fatalf("NewFileAuthProfileStore() error = %v", err)
	}
	snapshots, err := store.AuthProfileSnapshots(context.Background(), "xpk-fixture01")
	if err != nil {
		t.Fatalf("AuthProfileSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("AuthProfileSnapshots() = %d, want 1", len(snapshots))
	}
	if strings.Contains(snapshots[0].RedactedDisplay, "loaded-secret-token") {
		t.Fatalf("loaded RedactedDisplay leaked secret: %#v", snapshots[0])
	}
	if !snapshots[0].UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdatedAt = %s, want preserved %s", snapshots[0].UpdatedAt, updatedAt)
	}
	if snapshots[0].ExpiresAt == nil || !snapshots[0].ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v, want preserved %s", snapshots[0].ExpiresAt, expiresAt)
	}
}

func TestAuthProfileStoreConcurrentOperations(t *testing.T) {
	store := newTempAuthProfileStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			profileID := AuthProfileID(fmt.Sprintf("profile-%02d", i))
			_, _ = store.SetAuthProfile(ctx, AuthProfileUpdate{
				PackID:         "xpk-fixture01",
				ProfileID:      profileID,
				Kind:           AuthSecretKindBearer,
				Secret:         fmt.Sprintf("token-%02d", i),
				AllowedDomains: []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
			})
			_, _ = store.AuthProfileSnapshots(ctx, "xpk-fixture01")
			_, _ = store.ResolveAuthProfile(ctx, "xpk-fixture01", profileID, "https://api.fixture.invalid/path")
			if i%3 == 0 {
				_ = store.ClearAuthProfile(ctx, "xpk-fixture01", profileID)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent auth profile operations timed out")
	}
}

func newTempAuthProfileStore(t *testing.T) *FileAuthProfileStore {
	t.Helper()

	store, err := NewFileAuthProfileStore(filepath.Join(t.TempDir(), "extractor_auth.json"))
	if err != nil {
		t.Fatalf("NewFileAuthProfileStore() error = %v", err)
	}

	return store
}

func assertSnapshotRedacted(t *testing.T, snapshot AuthProfileSnapshot, forbidden string) {
	t.Helper()

	text := fmt.Sprintf("%#v %s", snapshot, snapshot.String())
	if strings.Contains(text, forbidden) {
		t.Fatalf("snapshot leaked %q: %s", forbidden, text)
	}
	if snapshot.RedactedDisplay == forbidden {
		t.Fatalf("RedactedDisplay leaked raw secret: %#v", snapshot)
	}
}
