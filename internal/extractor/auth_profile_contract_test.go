package extractor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHostAuthProfileSeedStatusResolveAndClear(t *testing.T) {
	store := newTempAuthProfileStore(t)
	descriptor := syntheticHostAuthProfileDescriptor()
	secret := "synthetic-bearer-secret"

	snapshot, err := SeedHostAuthProfile(context.Background(), store, descriptor, AuthSecretKindBearer, "Bearer "+secret, nil, "operator "+secret)
	if err != nil {
		t.Fatalf("SeedHostAuthProfile() error = %v", err)
	}
	if snapshot.PackID != descriptor.PackID || snapshot.ProfileID != descriptor.ProfileID || snapshot.Kind != AuthSecretKindBearer || !snapshot.HasSecret {
		t.Fatalf("snapshot = %#v, want descriptor bearer profile", snapshot)
	}
	assertNoRawSecret(t, fmt.Sprintf("%#v %s", snapshot, snapshot.String()), secret)

	status, err := GetHostAuthProfileStatus(context.Background(), store, descriptor)
	if err != nil {
		t.Fatalf("GetHostAuthProfileStatus() error = %v", err)
	}
	assertHostAuthStatusAvailable(t, status, descriptor, AuthSecretKindBearer, secret)

	resolved, err := store.ResolveAuthProfile(context.Background(), descriptor.PackID, descriptor.ProfileID, "https://files.example.test/download.bin")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderName != "Authorization" || resolved.HeaderValue != "Bearer "+secret {
		t.Fatalf("resolved = %#v, want bearer Authorization header", resolved)
	}

	status, err = ClearHostAuthProfile(context.Background(), store, descriptor)
	if err != nil {
		t.Fatalf("ClearHostAuthProfile() error = %v", err)
	}
	if status.Available || status.HasSecret || status.Kind != "" {
		t.Fatalf("status after clear = %#v, want unavailable", status)
	}
	assertNoRawSecret(t, fmt.Sprintf("%#v", status), secret)
}

func TestHostAuthProfileInvalidDescriptorFailuresAreGenericAndRedacted(t *testing.T) {
	store := newTempAuthProfileStore(t)
	secret := "descriptor-secret"

	tests := []struct {
		name       string
		descriptor HostAuthProfileDescriptor
	}{
		{name: "invalid pack", descriptor: HostAuthProfileDescriptor{PackID: "Invalid", ProfileID: "default", AllowedDomains: []DomainRule{{Host: "example.test"}}, StatusCheckURL: "https://example.test/status"}},
		{name: "invalid profile", descriptor: HostAuthProfileDescriptor{PackID: "xpk-alpha", ProfileID: "Default", AllowedDomains: []DomainRule{{Host: "example.test"}}, StatusCheckURL: "https://example.test/status"}},
		{name: "invalid domain", descriptor: HostAuthProfileDescriptor{PackID: "xpk-alpha", ProfileID: "default", AllowedDomains: []DomainRule{{Host: "evil..test"}}, StatusCheckURL: "https://example.test/status"}},
		{name: "http status url", descriptor: HostAuthProfileDescriptor{PackID: "xpk-alpha", ProfileID: "default", AllowedDomains: []DomainRule{{Host: "example.test"}}, StatusCheckURL: "http://example.test/status"}},
		{name: "status url outside scope", descriptor: HostAuthProfileDescriptor{PackID: "xpk-alpha", ProfileID: "default", AllowedDomains: []DomainRule{{Host: "example.test"}}, StatusCheckURL: "https://other.test/status"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SeedHostAuthProfile(context.Background(), store, tt.descriptor, AuthSecretKindBearer, secret, nil, "display "+secret)
			if err == nil {
				t.Fatal("SeedHostAuthProfile() error = nil, want descriptor failure")
			}
			assertNoRawSecret(t, err.Error(), secret)
		})
	}
}

func TestHostAuthProfileExpiredDeniedAndCRLFDoNotLeakSecrets(t *testing.T) {
	descriptor := syntheticHostAuthProfileDescriptor()

	store := newTempAuthProfileStore(t)
	_, err := SeedHostAuthProfile(context.Background(), store, descriptor, AuthSecretKindCookie, "sid=bad\r\nsecret", nil, "")
	if err == nil {
		t.Fatal("SeedHostAuthProfile() error = nil, want CR/LF rejection")
	}
	for _, forbidden := range []string{"sid=bad", "bad\r\nsecret"} {
		assertNoRawSecret(t, err.Error(), forbidden)
	}

	expiredSecret := "expired-host-auth-secret"
	expiresAt := time.Now().Add(-time.Minute)
	if _, err := SeedHostAuthProfile(context.Background(), store, descriptor, AuthSecretKindBearer, expiredSecret, &expiresAt, "expired "+expiredSecret); err != nil {
		t.Fatalf("SeedHostAuthProfile() expired error = %v", err)
	}
	status, err := GetHostAuthProfileStatus(context.Background(), store, descriptor)
	if err != nil {
		t.Fatalf("GetHostAuthProfileStatus() expired error = %v", err)
	}
	if status.Available || !status.HasSecret || status.ExpiresAt == nil {
		t.Fatalf("expired status = %#v, want unavailable with redacted metadata", status)
	}
	assertNoRawSecret(t, fmt.Sprintf("%#v", status), expiredSecret)

	deniedSecret := "denied-host-auth-secret"
	denied := descriptor
	denied.AllowedDomains = []DomainRule{{Host: "example.test"}}
	if _, err := SeedHostAuthProfile(context.Background(), store, denied, AuthSecretKindBearer, deniedSecret, nil, "denied"); err != nil {
		t.Fatalf("SeedHostAuthProfile() denied error = %v", err)
	}
	_, err = store.ResolveAuthProfile(context.Background(), denied.PackID, denied.ProfileID, "https://files.example.test/download.bin?token=query-secret")
	if err == nil {
		t.Fatal("ResolveAuthProfile() denied error = nil")
	}
	for _, forbidden := range []string{deniedSecret, "query-secret"} {
		assertNoRawSecret(t, err.Error(), forbidden)
	}
}

func TestHostAuthProfileDescriptorReturnsDefensiveDomainCopies(t *testing.T) {
	store := newTempAuthProfileStore(t)
	descriptor := syntheticHostAuthProfileDescriptor()
	if _, err := SeedHostAuthProfile(context.Background(), store, descriptor, AuthSecretKindBearer, "copy-secret", nil, ""); err != nil {
		t.Fatalf("SeedHostAuthProfile() error = %v", err)
	}
	status, err := GetHostAuthProfileStatus(context.Background(), store, descriptor)
	if err != nil {
		t.Fatalf("GetHostAuthProfileStatus() error = %v", err)
	}
	status.AllowedDomains[0].Host = "evil.test"
	descriptor.AllowedDomains[0].Host = "evil.test"
	next, err := GetHostAuthProfileStatus(context.Background(), store, syntheticHostAuthProfileDescriptor())
	if err != nil {
		t.Fatalf("GetHostAuthProfileStatus() second error = %v", err)
	}
	if next.AllowedDomains[0].Host != "example.test" {
		t.Fatalf("AllowedDomains was not defensively copied: %#v", next.AllowedDomains)
	}
}

func syntheticHostAuthProfileDescriptor() HostAuthProfileDescriptor {
	return HostAuthProfileDescriptor{
		PackID:         "xpk-alpha",
		ProfileID:      "default",
		AllowedDomains: []DomainRule{{Host: "example.test", IncludeSubdomains: true}},
		StatusCheckURL: "https://example.test/status",
	}
}

func assertHostAuthStatusAvailable(t *testing.T, status HostAuthProfileStatus, descriptor HostAuthProfileDescriptor, kind AuthSecretKind, forbidden string) {
	t.Helper()
	if status.PackID != descriptor.PackID || status.ProfileID != descriptor.ProfileID || !status.Available || !status.HasSecret || status.Kind != kind {
		t.Fatalf("status = %#v, want available %s descriptor profile", status, kind)
	}
	if len(status.AllowedDomains) != 1 || status.AllowedDomains[0].Host != "example.test" || !status.AllowedDomains[0].IncludeSubdomains {
		t.Fatalf("AllowedDomains = %#v, want example.test include_subdomains", status.AllowedDomains)
	}
	assertNoRawSecret(t, fmt.Sprintf("%#v", status), forbidden)
}

func assertNoRawSecret(t *testing.T, value string, forbidden string) {
	t.Helper()
	if forbidden != "" && strings.Contains(value, forbidden) {
		t.Fatalf("value leaked forbidden secret %q", forbidden)
	}
}
