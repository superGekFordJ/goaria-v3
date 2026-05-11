package extractor

import (
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
