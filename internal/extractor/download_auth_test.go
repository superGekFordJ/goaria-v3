package extractor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDownloadAuthPack(t *testing.T, mutate func(*Manifest)) VerifiedPack {
	t.Helper()
	return newTestVerifiedPackForRegistry(t, "xpk-alpha001", "opaque-1", []byte("download-auth-payload"), func(m *Manifest) {
		m.Capabilities = []Capability{CapabilityParseWASM, CapabilityHTTPFetch, CapabilityDownloadAuth}
		if mutate != nil {
			mutate(m)
		}
	})
}

func retainedDownloadAuthRef(t *testing.T, reg *DownloadAuthRegistry, invocation uint64, pack VerifiedPackIdentity, host string, token string) string {
	t.Helper()
	ref, err := reg.Register(invocation, pack, []byte(token))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := reg.Bind(invocation, ref, pack, host); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	reg.EndInvocation(invocation, true)
	return ref
}

func TestValidateDownloadAuthRefShape(t *testing.T) {
	valid := "dar-" + strings.Repeat("0123456789abcdef", 2)
	if err := validateDownloadAuthRef(valid); err != nil {
		t.Fatalf("validateDownloadAuthRef(%q) error = %v", valid, err)
	}

	cases := map[string]string{
		"empty":          "",
		"short":          "dar-" + strings.Repeat("a", 31),
		"long":           "dar-" + strings.Repeat("a", 33),
		"prefix only":    "dar-",
		"wrong prefix":   "xar-" + strings.Repeat("a", 32),
		"uppercase hex":  "dar-" + strings.Repeat("A", 32),
		"non hex":        "dar-" + strings.Repeat("g", 32),
		"raw token":      "tok-live-bearer",
		"bearer literal": "Bearer " + strings.Repeat("a", 32),
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateDownloadAuthRef(ref); err == nil {
				t.Fatalf("validateDownloadAuthRef(%q) error = nil, want rejection", ref)
			}
		})
	}
}

func TestValidateExtractOutputDownloadAuthRefShape(t *testing.T) {
	output := ExtractOutput{Items: []ExtractedItemRef{{
		URL:             "https://download.fixture.invalid/file.bin",
		DownloadAuthRef: "dar-" + strings.Repeat("a", 32),
	}}}
	if err := ValidateExtractOutput(output, ResourceLimits{MaxOutputItems: 10}); err != nil {
		t.Fatalf("ValidateExtractOutput() error = %v, want well-formed ref accepted", err)
	}

	output.Items[0].DownloadAuthRef = "tok-raw-bearer-secret"
	if err := ValidateExtractOutput(output, ResourceLimits{MaxOutputItems: 10}); err == nil {
		t.Fatal("ValidateExtractOutput() error = nil, want malformed download_auth_ref rejection")
	}
}

func TestValidateDownloadAuthToken(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "ordinary token", token: "fixture-guest-session-token"},
		{name: "single byte", token: "x"},
		{name: "empty", token: "", wantErr: true},
		{name: "bearer prefix lowercase", token: "bearer abc", wantErr: true},
		{name: "bearer prefix mixed case", token: "Bearer abc", wantErr: true},
		{name: "bearer prefix upper", token: "BEARER abc", wantErr: true},
		{name: "contains CR", token: "abc\rdef", wantErr: true},
		{name: "contains LF", token: "abc\ndef", wantErr: true},
		{name: "oversize", token: strings.Repeat("a", downloadAuthTokenMaxBytes+1), wantErr: true},
		{name: "max size", token: strings.Repeat("a", downloadAuthTokenMaxBytes)},
		{name: "invalid utf8", token: string([]byte{0xff, 0xfe}), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDownloadAuthToken([]byte(tc.token))
			if tc.wantErr && err == nil {
				t.Fatalf("validateDownloadAuthToken(%q) error = nil, want rejection", tc.token)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateDownloadAuthToken(%q) error = %v", tc.token, err)
			}
		})
	}
}

func TestDownloadAuthRegistryLifecycle(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()

	ref, err := reg.Register(7, pack.Identity, []byte("token-abc"))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := validateDownloadAuthRef(ref); err != nil {
		t.Fatalf("Register() minted malformed ref %q: %v", ref, err)
	}
	if err := reg.Bind(7, ref, pack.Identity, "files.fixture.invalid"); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	reg.EndInvocation(7, true)
	if reg.EntryCount() != 1 {
		t.Fatalf("EntryCount() = %d, want retained bound entry", reg.EntryCount())
	}
	if err := reg.Claim(ref, "holder-1"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	token, err := reg.Materialize(pack.Identity, ref, "files.fixture.invalid")
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	if token != "token-abc" {
		t.Fatalf("Materialize() token = %q", token)
	}
	reg.Release(ref, "holder-1")
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want freed after last release", reg.EntryCount())
	}
	if _, err := reg.Materialize(pack.Identity, ref, "files.fixture.invalid"); !errors.Is(err, ErrDownloadAuthUnknownRef) {
		t.Fatalf("Materialize() error = %v, want ErrDownloadAuthUnknownRef", err)
	}
}

func TestDownloadAuthRegistryUnboundPurgedOnCommittedEnd(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	if _, err := reg.Register(3, pack.Identity, []byte("token")); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	reg.EndInvocation(3, true)
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want unbound entry purged", reg.EntryCount())
	}
}

func TestDownloadAuthRegistryUncommittedEndPurges(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	ref, err := reg.Register(4, pack.Identity, []byte("token"))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := reg.Bind(4, ref, pack.Identity, "files.fixture.invalid"); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	reg.EndInvocation(4, false)
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want invocation purged", reg.EntryCount())
	}
}

func TestDownloadAuthRegistryBindRejectsForeignInvocation(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	ref, err := reg.Register(1, pack.Identity, []byte("token"))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := reg.Bind(2, ref, pack.Identity, "files.fixture.invalid"); err == nil {
		t.Fatal("Bind() error = nil, want cross-invocation rejection")
	}

	reg.EndInvocation(1, false)
	if _, err := reg.Register(1, pack.Identity, []byte("token")); err != nil {
		t.Fatalf("re-Register() error = %v", err)
	}
}

func TestDownloadAuthRegistryBindRejectsRetainedEntry(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	ref := retainedDownloadAuthRef(t, reg, 9, pack.Identity, "files.fixture.invalid", "token")
	if err := reg.Bind(9, ref, pack.Identity, "files.fixture.invalid"); err == nil {
		t.Fatal("Bind() error = nil, want retained-entry rejection")
	}
}

func TestDownloadAuthRegistryBindRejectsForeignPack(t *testing.T) {
	packA := testDownloadAuthPack(t, nil)
	packB := testDownloadAuthPack(t, func(m *Manifest) {
		m.PackID = "xpk-bravo001"
	})
	reg := NewDownloadAuthRegistry()
	ref, err := reg.Register(5, packA.Identity, []byte("token"))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := reg.Bind(5, ref, packB.Identity, "files.fixture.invalid"); err == nil {
		t.Fatal("Bind() error = nil, want cross-pack rejection")
	}
}

func TestDownloadAuthRegistryExpiry(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	start := time.Now()
	reg.now = func() time.Time { return start }

	ref := retainedDownloadAuthRef(t, reg, 11, pack.Identity, "files.fixture.invalid", "token")
	reg.now = func() time.Time { return start.Add(downloadAuthEntryTTL + time.Second) }

	if err := reg.Claim(ref, "holder"); !errors.Is(err, ErrDownloadAuthUnknownRef) {
		t.Fatalf("Claim() error = %v, want expired ref swept", err)
	}
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want expired entry removed", reg.EntryCount())
	}
}

func TestDownloadAuthRegistryPerInvocationLimit(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	for i := range downloadAuthPendingPerInvoke {
		if _, err := reg.Register(21, pack.Identity, []byte("token")); err != nil {
			t.Fatalf("Register() #%d error = %v", i, err)
		}
	}
	if _, err := reg.Register(21, pack.Identity, []byte("token")); !errors.Is(err, ErrDownloadAuthInvocationLimit) {
		t.Fatalf("Register() error = %v, want ErrDownloadAuthInvocationLimit", err)
	}
	if _, err := reg.Register(22, pack.Identity, []byte("token")); err != nil {
		t.Fatalf("Register() for other invocation error = %v", err)
	}
}

func TestDownloadAuthRegistryGlobalCapacitySweepsExpiredFirst(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	start := time.Now()
	reg.now = func() time.Time { return start }

	for i := range downloadAuthRegistryCapacity {
		if _, err := reg.Register(uint64(i+1), pack.Identity, []byte("token")); err != nil {
			t.Fatalf("Register() #%d error = %v", i, err)
		}
	}
	if _, err := reg.Register(9999, pack.Identity, []byte("token")); !errors.Is(err, ErrDownloadAuthRegistryFull) {
		t.Fatalf("Register() error = %v, want ErrDownloadAuthRegistryFull at capacity", err)
	}

	reg.now = func() time.Time { return start.Add(downloadAuthEntryTTL + time.Second) }
	if _, err := reg.Register(9999, pack.Identity, []byte("token")); err != nil {
		t.Fatalf("Register() after expiry error = %v, want expired entries swept first", err)
	}
}

func TestDownloadAuthRegistryMaterializeAdmission(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	other := testDownloadAuthPack(t, func(m *Manifest) { m.PackID = "xpk-bravo001" })
	reg := NewDownloadAuthRegistry()

	// Pending (invocation still open) entries are never materializable.
	pendingRef, err := reg.Register(31, pack.Identity, []byte("token"))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := reg.Bind(31, pendingRef, pack.Identity, "files.fixture.invalid"); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if _, err := reg.Materialize(pack.Identity, pendingRef, "files.fixture.invalid"); !errors.Is(err, ErrDownloadAuthNotMaterializable) {
		t.Fatalf("Materialize() error = %v, want invocation-scoped rejection", err)
	}
	reg.EndInvocation(31, true)

	// Retained but unclaimed.
	if _, err := reg.Materialize(pack.Identity, pendingRef, "files.fixture.invalid"); !errors.Is(err, ErrDownloadAuthNotMaterializable) {
		t.Fatalf("Materialize() error = %v, want no-holders rejection", err)
	}
	if err := reg.Claim(pendingRef, "h"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	// Wrong host.
	if _, err := reg.Materialize(pack.Identity, pendingRef, "evil.fixture.invalid"); !errors.Is(err, ErrDownloadAuthNotMaterializable) {
		t.Fatalf("Materialize() error = %v, want host rejection", err)
	}
	// Wrong pack identity.
	if _, err := reg.Materialize(other.Identity, pendingRef, "files.fixture.invalid"); !errors.Is(err, ErrDownloadAuthNotMaterializable) {
		t.Fatalf("Materialize() error = %v, want pack rejection", err)
	}
	// ValidateRef mirrors the checks without returning the token.
	if err := reg.ValidateRef(pack.Identity, pendingRef, "files.fixture.invalid"); err != nil {
		t.Fatalf("ValidateRef() error = %v", err)
	}
	if err := reg.ValidateRef(other.Identity, pendingRef, "files.fixture.invalid"); err == nil {
		t.Fatal("ValidateRef() error = nil, want pack rejection")
	}
}

func TestDownloadAuthRegistryReleaseIgnoresUnknownHolder(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	ref := retainedDownloadAuthRef(t, reg, 45, pack.Identity, "files.fixture.invalid", "token")

	// A retained entry with zero holders survives a foreign-key release.
	reg.Release(ref, "never-claimed")
	if reg.EntryCount() != 1 {
		t.Fatalf("EntryCount() = %d, want unknown-holder release to be a no-op", reg.EntryCount())
	}

	if err := reg.Claim(ref, "holder-a"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := reg.Claim(ref, "holder-b"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	// Unknown keys must not count as a release of the real holders.
	reg.Release(ref, "bogus")
	if reg.EntryCount() != 1 {
		t.Fatalf("EntryCount() = %d, want entry retained with live holders", reg.EntryCount())
	}
	reg.Release(ref, "holder-a")
	if reg.EntryCount() != 1 {
		t.Fatalf("EntryCount() = %d, want retained until last holder", reg.EntryCount())
	}
	reg.Release(ref, "holder-b")
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want freed after last holder release", reg.EntryCount())
	}
}

func TestDownloadAuthRegistryClaimIdempotent(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	ref := retainedDownloadAuthRef(t, reg, 41, pack.Identity, "files.fixture.invalid", "token")

	for i := range 3 {
		if err := reg.Claim(ref, "same-holder"); err != nil {
			t.Fatalf("Claim() #%d error = %v", i, err)
		}
	}
	reg.Release(ref, "same-holder")
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want idempotent claim released by one Release", reg.EntryCount())
	}
}

func TestDownloadAuthRegistryInvalidate(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	ref := retainedDownloadAuthRef(t, reg, 51, pack.Identity, "files.fixture.invalid", "token")
	if err := reg.Claim(ref, "holder"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	reg.Invalidate()
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want invalidated", reg.EntryCount())
	}
	if err := reg.Claim(ref, "holder2"); !errors.Is(err, ErrDownloadAuthUnknownRef) {
		t.Fatalf("Claim() error = %v, want stale ref rejection after Invalidate", err)
	}
	if _, err := reg.Materialize(pack.Identity, ref, "files.fixture.invalid"); !errors.Is(err, ErrDownloadAuthUnknownRef) {
		t.Fatalf("Materialize() error = %v, want stale ref rejection after Invalidate", err)
	}
}

func TestDownloadAuthRegistryConcurrentClaimRelease(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	ref := retainedDownloadAuthRef(t, reg, 61, pack.Identity, "files.fixture.invalid", "token")
	// A permanent holder keeps the entry alive while workers churn claims.
	if err := reg.Claim(ref, "permanent"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "holder-" + string(rune('a'+i))
			if err := reg.Claim(ref, key); err != nil {
				t.Errorf("Claim() error = %v", err)
				return
			}
			reg.Release(ref, key)
		}(i)
	}
	wg.Wait()
	reg.Release(ref, "permanent")
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want all claims released", reg.EntryCount())
	}
}

func TestDispatcherBindsAndMaterializesDownloadAuth(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: reg})

	ref, err := reg.Register(42, pack.Identity, []byte("token-abc"))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	output := ExtractOutput{Items: []ExtractedItemRef{{
		URL:             "https://files.fixture.invalid/file.bin",
		Filename:        "file.bin",
		DownloadAuthRef: ref,
	}}}
	output.SetInvocationID(42)

	items, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/s/abc", pack, nil, output)
	if err != nil {
		t.Fatalf("boundItemsFromExtractOutput() error = %v", err)
	}
	if len(items) != 1 || items[0].DownloadAuthRef != ref {
		t.Fatalf("items = %#v", items)
	}
	// Committed end retained the bound entry.
	if reg.EntryCount() != 1 {
		t.Fatalf("EntryCount() = %d, want retained bound entry", reg.EntryCount())
	}
	if err := dispatcher.ClaimDownloadAuth(ref, "holder-1"); err != nil {
		t.Fatalf("ClaimDownloadAuth() error = %v", err)
	}
	headers, err := dispatcher.BuildAria2Headers(context.Background(), items[0])
	if err != nil {
		t.Fatalf("BuildAria2Headers() error = %v", err)
	}
	if len(headers) != 1 || headers[0] != "Authorization: Bearer token-abc" {
		t.Fatalf("headers = %#v, want materialized bearer", headers)
	}
	if err := dispatcher.ValidateDownloadAuthBinding(items[0]); err != nil {
		t.Fatalf("ValidateDownloadAuthBinding() error = %v", err)
	}
	dispatcher.ReleaseDownloadAuth(ref, "holder-1")
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want released", reg.EntryCount())
	}
}

func TestDispatcherDownloadAuthBindingRejections(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	other := testDownloadAuthPack(t, func(m *Manifest) { m.PackID = "xpk-bravo001" })
	validRef := "dar-" + strings.Repeat("b", 32)

	newDispatcher := func() (*AddTaskDispatcher, *DownloadAuthRegistry) {
		reg := NewDownloadAuthRegistry()
		return NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: reg}), reg
	}
	bindOutput := func(ref string, invocation uint64) ExtractOutput {
		output := ExtractOutput{Items: []ExtractedItemRef{{
			URL:             "https://files.fixture.invalid/file.bin",
			DownloadAuthRef: ref,
		}}}
		output.SetInvocationID(invocation)
		return output
	}

	t.Run("malformed ref", func(t *testing.T) {
		dispatcher, reg := newDispatcher()
		_, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, bindOutput("tok-raw", 1))
		if err == nil || !strings.Contains(err.Error(), "download_auth_ref") {
			t.Fatalf("error = %v, want download_auth_ref rejection", err)
		}
		if reg.EntryCount() != 0 {
			t.Fatalf("EntryCount() = %d", reg.EntryCount())
		}
	})

	t.Run("forged ref", func(t *testing.T) {
		dispatcher, reg := newDispatcher()
		_, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, bindOutput(validRef, 1))
		if err == nil {
			t.Fatal("error = nil, want forged ref rejection")
		}
		if reg.EntryCount() != 0 {
			t.Fatalf("EntryCount() = %d", reg.EntryCount())
		}
	})

	t.Run("missing capability", func(t *testing.T) {
		noCap := testDownloadAuthPack(t, func(m *Manifest) {
			m.Capabilities = []Capability{CapabilityParseWASM, CapabilityHTTPFetch}
		})
		dispatcher, reg := newDispatcher()
		ref, err := reg.Register(1, noCap.Identity, []byte("token"))
		if err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		_, err = dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", noCap, nil, bindOutput(ref, 1))
		if err == nil || !strings.Contains(err.Error(), string(CapabilityDownloadAuth)) {
			t.Fatalf("error = %v, want capability rejection", err)
		}
		if reg.EntryCount() != 0 {
			t.Fatalf("EntryCount() = %d, want failed binding purged", reg.EntryCount())
		}
	})

	t.Run("combined auth profile ref", func(t *testing.T) {
		dispatcher, _ := newDispatcher()
		output := bindOutput(validRef, 1)
		output.Items[0].AuthProfileRef = "apr-fixture01"
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, output); err == nil {
			t.Fatal("error = nil, want mutual-exclusion rejection")
		}
	})

	t.Run("combined header profile ref", func(t *testing.T) {
		dispatcher, _ := newDispatcher()
		output := bindOutput(validRef, 1)
		output.Items[0].HeaderProfileRef = "hpr-fixture01"
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, output); err == nil {
			t.Fatal("error = nil, want mutual-exclusion rejection")
		}
	})

	t.Run("cross invocation", func(t *testing.T) {
		dispatcher, reg := newDispatcher()
		ref, err := reg.Register(1, pack.Identity, []byte("token"))
		if err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, bindOutput(ref, 2)); err == nil {
			t.Fatal("error = nil, want cross-invocation rejection")
		}
	})

	t.Run("cross pack", func(t *testing.T) {
		dispatcher, reg := newDispatcher()
		ref, err := reg.Register(1, other.Identity, []byte("token"))
		if err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, bindOutput(ref, 1)); err == nil {
			t.Fatal("error = nil, want cross-pack rejection")
		}
	})

	t.Run("binding failure purges invocation", func(t *testing.T) {
		dispatcher, reg := newDispatcher()
		if _, err := reg.Register(1, pack.Identity, []byte("token")); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, bindOutput(validRef, 1)); err == nil {
			t.Fatal("error = nil, want binding failure")
		}
		if reg.EntryCount() != 0 {
			t.Fatalf("EntryCount() = %d, want uncommitted purge", reg.EntryCount())
		}
	})
}

func TestDispatcherBuildAria2HeadersDownloadAuthErrors(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: reg})
	ref := retainedDownloadAuthRef(t, reg, 71, pack.Identity, "files.fixture.invalid", "token")

	item := ResolvedAddItem{
		PackManifest:    pack.Manifest,
		PackIdentity:    pack.Identity,
		URL:             "https://files.fixture.invalid/file.bin",
		DownloadAuthRef: ref,
	}
	// No claim: materialization must fail closed rather than emit the token.
	if _, err := dispatcher.BuildAria2Headers(context.Background(), item); err == nil {
		t.Fatal("BuildAria2Headers() error = nil, want unclaimed ref rejection")
	}
	item.DownloadAuthRef = "dar-" + strings.Repeat("f", 32)
	if _, err := dispatcher.BuildAria2Headers(context.Background(), item); err == nil {
		t.Fatal("BuildAria2Headers() error = nil, want unknown ref rejection")
	}
}

func TestDispatcherCredentialedItemRequiresHTTPS(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)

	bindOutput := func(url string, mutate func(*ExtractedItemRef), invocation uint64) ExtractOutput {
		ref := ExtractedItemRef{URL: url}
		if mutate != nil {
			mutate(&ref)
		}
		output := ExtractOutput{Items: []ExtractedItemRef{ref}}
		output.SetInvocationID(invocation)
		return output
	}

	t.Run("http download_auth_ref rejected at binding", func(t *testing.T) {
		reg := NewDownloadAuthRegistry()
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: reg})
		ref, err := reg.Register(1, pack.Identity, []byte("token"))
		if err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		output := bindOutput("http://files.fixture.invalid/file.bin", func(item *ExtractedItemRef) {
			item.DownloadAuthRef = ref
		}, 1)
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, output); err == nil {
			t.Fatal("binding error = nil, want http credentialed url rejection")
		}
		if reg.EntryCount() != 0 {
			t.Fatalf("EntryCount() = %d, want failed invocation purged", reg.EntryCount())
		}
	})

	t.Run("http auth_profile_ref rejected at binding", func(t *testing.T) {
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: NewDownloadAuthRegistry()})
		output := bindOutput("http://files.fixture.invalid/file.bin", func(item *ExtractedItemRef) {
			item.AuthProfileRef = "apr-fixture01"
		}, 1)
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, output); err == nil {
			t.Fatal("binding error = nil, want http credentialed url rejection")
		}
	})

	t.Run("http header_profile_ref rejected at binding", func(t *testing.T) {
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: NewDownloadAuthRegistry()})
		output := bindOutput("http://files.fixture.invalid/file.bin", func(item *ExtractedItemRef) {
			item.HeaderProfileRef = "hpr-fixture01"
		}, 1)
		if _, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, output); err == nil {
			t.Fatal("binding error = nil, want http credentialed url rejection")
		}
	})

	t.Run("plain http item without refs still allowed", func(t *testing.T) {
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: NewDownloadAuthRegistry()})
		items, err := dispatcher.boundItemsFromExtractOutput("https://share.fixture.invalid/x", pack, nil, bindOutput("http://files.fixture.invalid/file.bin", nil, 1))
		if err != nil {
			t.Fatalf("binding error = %v, want plain http item accepted", err)
		}
		if len(items) != 1 {
			t.Fatalf("items = %#v", items)
		}
	})

	t.Run("materialize rejects http credentialed item", func(t *testing.T) {
		reg := NewDownloadAuthRegistry()
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: reg})
		ref := retainedDownloadAuthRef(t, reg, 2, pack.Identity, "files.fixture.invalid", "token")
		if err := reg.Claim(ref, "holder"); err != nil {
			t.Fatalf("Claim() error = %v", err)
		}
		item := ResolvedAddItem{
			PackManifest:    pack.Manifest,
			PackIdentity:    pack.Identity,
			URL:             "http://files.fixture.invalid/file.bin",
			DownloadAuthRef: ref,
		}
		if _, err := dispatcher.BuildAria2Headers(context.Background(), item); err == nil {
			t.Fatal("BuildAria2Headers() error = nil, want http credentialed rejection")
		}
		if err := dispatcher.ValidateDownloadAuthBinding(item); err == nil {
			t.Fatal("ValidateDownloadAuthBinding() error = nil, want http credentialed rejection")
		}
	})
}

// rollbackFakeDispatcher records claim/release calls so partial-mint
// rollback is observable without a real registry.
type rollbackFakeDispatcher struct {
	items    []ResolvedAddItem
	claims   map[string]string // holderKey → downloadAuthRef
	releases map[string]string
	failRef  string
}

func (f *rollbackFakeDispatcher) Resolve(_ context.Context, rawURL string) (AddTaskResolution, error) {
	return AddTaskResolution{Matched: true, SourceURL: rawURL, Items: f.items}, nil
}

func (f *rollbackFakeDispatcher) BuildAria2Headers(_ context.Context, _ ResolvedAddItem) ([]string, error) {
	return nil, nil
}

func (f *rollbackFakeDispatcher) AuthRuntimeRequestsForSource(_ context.Context, _ string) ([]HostAuthRuntimeRequest, error) {
	return nil, nil
}

func (f *rollbackFakeDispatcher) ClaimDownloadAuth(ref string, holderKey string) error {
	if ref == "" {
		return nil
	}
	if ref == f.failRef {
		return errors.New("download auth ref is unknown")
	}
	f.claims[holderKey] = ref
	return nil
}

func (f *rollbackFakeDispatcher) ReleaseDownloadAuth(ref string, holderKey string) {
	if ref == "" {
		return
	}
	f.releases[holderKey] = ref
}

func (f *rollbackFakeDispatcher) ValidateDownloadAuthBinding(_ ResolvedAddItem) error {
	return nil
}

func TestTasksAdapterResolveRollsBackMintedClaims(t *testing.T) {
	firstRef := "dar-" + strings.Repeat("a", 32)
	secondRef := "dar-" + strings.Repeat("b", 32)
	dispatcher := &rollbackFakeDispatcher{
		claims:   make(map[string]string),
		releases: make(map[string]string),
		failRef:  secondRef,
		items: []ResolvedAddItem{
			{URL: "https://files.fixture.invalid/a.bin", DownloadAuthRef: firstRef},
			{URL: "https://files.fixture.invalid/b.bin", DownloadAuthRef: secondRef},
		},
	}
	adapter := NewTasksAdapter(dispatcher, nil)

	if _, err := adapter.Resolve(context.Background(), "https://share.fixture.invalid/s/abc"); err == nil {
		t.Fatal("Resolve() error = nil, want second-item claim failure")
	}
	if len(dispatcher.claims) != 1 {
		t.Fatalf("claims = %#v, want exactly the first item claimed", dispatcher.claims)
	}
	if len(dispatcher.releases) != 1 {
		t.Fatalf("releases = %#v, want the minted claim rolled back", dispatcher.releases)
	}
	for key, ref := range dispatcher.releases {
		if ref != firstRef {
			t.Fatalf("released ref = %q, want %q", ref, firstRef)
		}
		if !strings.HasPrefix(key, "ref:") {
			t.Fatalf("released holder key = %q, want ref: prefix", key)
		}
	}
	if len(adapter.resolvedItems) != 0 {
		t.Fatalf("resolvedItems = %d entries, want rollback emptied the map", len(adapter.resolvedItems))
	}
}

func TestTasksAdapterClaimsAndReleasesDownloadAuth(t *testing.T) {
	pack := testDownloadAuthPack(t, nil)
	reg := NewDownloadAuthRegistry()
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{DownloadAuth: reg})
	adapter := NewTasksAdapter(dispatcher, nil)
	ref := retainedDownloadAuthRef(t, reg, 81, pack.Identity, "files.fixture.invalid", "token-xyz")

	item := ResolvedAddItem{
		PackManifest:    pack.Manifest,
		PackIdentity:    pack.Identity,
		URL:             "https://files.fixture.invalid/file.bin",
		DownloadAuthRef: ref,
	}
	neutral, err := adapter.Mint(item)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if neutral.DownloadAuthRef != ref {
		t.Fatalf("neutral ref = %q, want %q", neutral.DownloadAuthRef, ref)
	}
	if err := adapter.ValidateItemAuthPolicy(neutral); err != nil {
		t.Fatalf("ValidateItemAuthPolicy() error = %v", err)
	}
	headers, err := adapter.BuildHeaders(context.Background(), neutral)
	if err != nil {
		t.Fatalf("BuildHeaders() error = %v", err)
	}
	if len(headers) != 1 || headers[0] != "Authorization: Bearer token-xyz" {
		t.Fatalf("headers = %#v", headers)
	}
	adapter.Release(neutral.Ref)
	if reg.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want neutral ref release to free entry", reg.EntryCount())
	}
	if _, err := adapter.Mint(item); err == nil {
		t.Fatal("Mint() error = nil, want claim failure on stale ref")
	}
}
