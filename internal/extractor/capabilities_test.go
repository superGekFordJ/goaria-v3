package extractor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCapabilityGuardAllowsHTTPFetchForAllowedDomains(t *testing.T) {
	manifest := validCapabilityManifest()

	for _, rawURL := range []string{
		"https://fixture.invalid/d/abc",
		"https://api.fixture.invalid/path",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if err := ValidateCapabilityURL(CapabilityContext{
				PackID:     "fixturepack",
				Manifest:   manifest,
				Capability: CapabilityHTTPFetch,
			}, rawURL); err != nil {
				t.Fatalf("ValidateCapabilityURL() error = %v", err)
			}
		})
	}
}

func TestCapabilityGuardRejectsMissingHTTPFetchCapability(t *testing.T) {
	manifest := validCapabilityManifest()
	manifest.Capabilities = []Capability{CapabilityParseWASM}

	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:     "fixturepack",
		Manifest:   manifest,
		Capability: CapabilityHTTPFetch,
	}, "https://fixture.invalid/d/abc"); err == nil {
		t.Fatal("ValidateCapabilityURL() error = nil, want error")
	}
}

func TestCapabilityGuardRejectsExactDomainSubdomainWhenDisabled(t *testing.T) {
	manifest := validCapabilityManifest()
	manifest.Domains = []DomainRule{{Host: "fixture.invalid"}}

	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:     "fixturepack",
		Manifest:   manifest,
		Capability: CapabilityHTTPFetch,
	}, "https://api.fixture.invalid/path"); err == nil {
		t.Fatal("ValidateCapabilityURL() error = nil, want error")
	}
}

func TestCapabilityGuardRejectsUnsafeURLs(t *testing.T) {
	manifest := validCapabilityManifest()

	unsafeURLs := []string{
		"://bad-url",
		"ftp://fixture.invalid/path",
		"https://user:pass@fixture.invalid/path",
		"https://127.0.0.1/path",
		"https://[::1]/path",
		"https://evilfixture.invalid/",
		"https://fixture.invalid.evil.test/path",
		"https://fixture.invalid./path",
		"https://fixture.invalid:bad/path",
	}

	for _, rawURL := range unsafeURLs {
		t.Run(rawURL, func(t *testing.T) {
			if err := ValidateCapabilityURL(CapabilityContext{
				PackID:     "fixturepack",
				Manifest:   manifest,
				Capability: CapabilityHTTPFetch,
			}, rawURL); err == nil {
				t.Fatal("ValidateCapabilityURL() error = nil, want error")
			}
		})
	}
}

func TestCapabilityGuardDoesNotTreatAliasRefsAsDomains(t *testing.T) {
	manifest := validAliasTestManifest(nil)

	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:     manifest.PackID,
		Manifest:   manifest,
		Capability: CapabilityHTTPFetch,
	}, "https://dpr-alpha001/path"); err == nil {
		t.Fatal("ValidateCapabilityURL() error = nil, want alias ref not treated as domain")
	}
	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:     manifest.PackID,
		Manifest:   manifest,
		Capability: CapabilityHTTPFetch,
	}, "bpr-alpha001"); err == nil {
		t.Fatal("ValidateCapabilityURL() error = nil, want opaque broker ref not treated as URL/domain")
	}
}

func TestCapabilityGuardAllowsAliasBrokerDomainWithPolicy(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	resolver := &fakeHostPolicyResolver{policy: validResolvedHostPolicy(identity, manifest)}

	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:             manifest.PackID,
		Manifest:           manifest,
		Capability:         CapabilityHTTPFetch,
		PackIdentity:       identity,
		HostPolicyResolver: resolver,
	}, "https://api.alpha.test/path"); err != nil {
		t.Fatalf("ValidateCapabilityURL() error = %v", err)
	}
}

func TestCapabilityGuardAliasPolicyDenials(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	base := validResolvedHostPolicy(identity, manifest)
	tests := []struct {
		name     string
		policy   extractorResolvedHostPolicyMutator
		identity VerifiedPackIdentity
		rawURL   string
	}{
		{name: "identity mismatch", policy: func(policy *ResolvedHostPolicy) { policy.PackIdentity.AssetSHA256 = strings.Repeat("9", 64) }, identity: identity, rawURL: "https://api.alpha.test/path"},
		{name: "capability mismatch", policy: func(policy *ResolvedHostPolicy) {
			policy.AllowedCapabilities = []Capability{CapabilityParseWASM, CapabilityAuthProfile}
		}, identity: identity, rawURL: "https://api.alpha.test/path"},
		{name: "ingress only denied", policy: nil, identity: identity, rawURL: "https://share.alpha.test/path"},
		{name: "malformed policy domain", policy: func(policy *ResolvedHostPolicy) { policy.BrokerDomains = []DomainRule{{Host: "*.alpha.test"}} }, identity: identity, rawURL: "https://api.alpha.test/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := cloneResolvedHostPolicy(base)
			if tt.policy != nil {
				tt.policy(&policy)
			}
			err := ValidateCapabilityURL(CapabilityContext{
				PackID:             manifest.PackID,
				Manifest:           manifest,
				Capability:         CapabilityHTTPFetch,
				PackIdentity:       tt.identity,
				HostPolicyResolver: &fakeHostPolicyResolver{policy: policy},
			}, tt.rawURL)
			if err == nil {
				t.Fatal("ValidateCapabilityURL() error = nil, want fail closed")
			}
		})
	}
}

func TestCapabilityGuardAliasResolverUsesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolver := HostPolicyResolver(hostPolicyResolverFunc(func(context.Context, HostPolicyRequest) (ResolvedHostPolicy, error) {
		return ResolvedHostPolicy{}, context.Canceled
	}))
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)

	_, err := allowedHTTPURLForAliasPolicy(ctx, manifest, identity, resolver, CapabilityHTTPFetch, "https://api.alpha.test/path")
	if err == nil {
		t.Fatal("allowedHTTPURLForAliasPolicy() error = nil, want cancellation denial")
	}
}

type extractorResolvedHostPolicyMutator func(*ResolvedHostPolicy)

type hostPolicyResolverFunc func(context.Context, HostPolicyRequest) (ResolvedHostPolicy, error)

func (fn hostPolicyResolverFunc) ResolveHostPolicy(ctx context.Context, request HostPolicyRequest) (ResolvedHostPolicy, error) {
	return fn(ctx, request)
}

func TestRedactSensitiveRemovesTokensCookiesAndQuerySecrets(t *testing.T) {
	knownBearer := "bearer-raw-token-123"
	knownCookie := "sessionid=raw-cookie-value"
	input := "request failed for https://api.fixture.invalid/path?token=abc123&access_token=def456&auth=ghi789&key=jkl012&secret=mno345&safe=value with Authorization: Bearer " + knownBearer + "; Cookie: " + knownCookie + "; Set-Cookie: other=raw-set-cookie; X-Api-Key: custom-secret"

	redacted := RedactSensitive(input, knownBearer, knownCookie, "custom-secret", "other=raw-set-cookie")
	for _, forbidden := range []string{
		knownBearer,
		knownCookie,
		"abc123",
		"def456",
		"ghi789",
		"jkl012",
		"mno345",
		"custom-secret",
		"raw-set-cookie",
	} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("RedactSensitive() leaked %q in %q", forbidden, redacted)
		}
	}

	for _, expected := range []string{
		"token=[REDACTED]",
		"access_token=[REDACTED]",
		"auth=[REDACTED]",
		"key=[REDACTED]",
		"secret=[REDACTED]",
		"Authorization: [REDACTED]",
		"Cookie: [REDACTED]",
		"Set-Cookie: [REDACTED]",
		"X-Api-Key: [REDACTED]",
	} {
		if !strings.Contains(redacted, expected) {
			t.Fatalf("RedactSensitive() = %q, want to contain %q", redacted, expected)
		}
	}
}

func TestRedactSensitiveRedactsSensitiveHeadersThroughLineEnd(t *testing.T) {
	input := strings.Join([]string{
		"Authorization: Bearer token-part-1 token-part-2; still-secret",
		"Cookie: sid=abc; refresh=def; theme=public",
		"Set-Cookie: sid=server-secret; Path=/; HttpOnly; Secure",
		"Proxy-Authorization: Basic dXNlcjpwYXNz with suffix",
		"Content-Type: text/plain",
	}, "\n")

	redacted := RedactSensitive(input)
	for _, forbidden := range []string{
		"token-part-1",
		"token-part-2",
		"still-secret",
		"sid=abc",
		"refresh=def",
		"theme=public",
		"server-secret",
		"HttpOnly",
		"dXNlcjpwYXNz",
		"with suffix",
	} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("RedactSensitive() leaked %q in %q", forbidden, redacted)
		}
	}
	for _, expected := range []string{
		"Authorization: [REDACTED]",
		"Cookie: [REDACTED]",
		"Set-Cookie: [REDACTED]",
		"Proxy-Authorization: [REDACTED]",
		"Content-Type: text/plain",
	} {
		if !strings.Contains(redacted, expected) {
			t.Fatalf("RedactSensitive() = %q, want %q", redacted, expected)
		}
	}
}

func TestRedactSensitiveRedactsAdjacentSensitiveHeaders(t *testing.T) {
	input := "Authorization: Bearer first Cookie: sid=second Set-Cookie: third=secret Proxy-Authorization: Basic fourth"
	redacted := RedactSensitive(input)
	for _, forbidden := range []string{"first", "sid=second", "third=secret", "fourth"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("RedactSensitive() leaked %q in %q", forbidden, redacted)
		}
	}
	if strings.Count(redacted, redactedMarker) != 4 {
		t.Fatalf("RedactSensitive() = %q, want four redaction markers", redacted)
	}
}

func TestRedactedErrorWrapsAndRedacts(t *testing.T) {
	err := redactedError(errors.New("Authorization: Bearer raw-token"), "raw-token")
	if err == nil {
		t.Fatal("redactedError() = nil, want error")
	}
	if strings.Contains(err.Error(), "raw-token") {
		t.Fatalf("redactedError() leaked token: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("redactedError() = %v, want redaction marker", err)
	}
}

func validCapabilityManifest() Manifest {
	manifest := validTestManifest()
	manifest.Capabilities = []Capability{CapabilityHTTPFetch, CapabilityAuthProfile}
	manifest.Domains = []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}

	return manifest
}
