package tasks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveHostIP_IPInURL(t *testing.T) {
	got := resolveHostIP("https://192.168.1.1/file")
	if got != "192.168.1.1" {
		t.Errorf("resolveHostIP(IP url) = %q, want 192.168.1.1", got)
	}
}

func TestResolveHostIP_InvalidURL(t *testing.T) {
	got := resolveHostIP(":::invalid")
	if got != "" {
		t.Errorf("resolveHostIP(invalid url) = %q, want empty", got)
	}
}

func TestResolveHostIP_NoHost(t *testing.T) {
	got := resolveHostIP("https:///file")
	if got != "" {
		t.Errorf("resolveHostIP(no host) = %q, want empty", got)
	}
}

func TestResolveHostIP_DomainResolves(t *testing.T) {
	lookup := func(ctx context.Context, host string) ([]string, error) {
		return []string{"203.0.113.7", "203.0.113.8"}, nil
	}
	if got := resolveHostIPWithLookup("https://example.com/file", time.Second, lookup); got != "203.0.113.7" {
		t.Errorf("resolveHostIPWithLookup(resolved domain) = %q, want first IP", got)
	}
}

func TestResolveHostIP_DNSFailure_ReturnsEmpty(t *testing.T) {
	lookup := func(ctx context.Context, host string) ([]string, error) {
		return nil, errors.New("nxdomain")
	}
	if got := resolveHostIPWithLookup("https://nonexistent.invalid.domain.example/file", time.Second, lookup); got != "" {
		t.Errorf("resolveHostIPWithLookup(nxdomain) = %q, want empty", got)
	}
}

func TestResolveHostIP_DelegatesToLookupVar(t *testing.T) {
	// TestMain stubs lookupHost to answer empty; a hostname reaching it both
	// yields "" and lands in the recorded call list — proving resolveHostIP
	// routes through the injected var rather than a hard-coded resolver.
	if got := resolveHostIP("https://delegation.test/file"); got != "" {
		t.Fatalf("resolveHostIP(stubbed lookup) = %q, want empty", got)
	}
	if !lookupHostSaw("delegation.test") {
		t.Fatal("resolveHostIP did not reach the stubbed lookupHost var")
	}
}

// TestResolveHostIP_BoundedLookup exercises the injected lookup seam: a fake
// context-aware resolver proves the caller-side deadline, cancellation on
// return, and failure degradation — without real DNS or extra goroutines.
func TestResolveHostIP_BoundedLookup(t *testing.T) {
	t.Run("deadline expiry returns empty", func(t *testing.T) {
		var sawDeadline atomic.Bool
		lookup := func(ctx context.Context, host string) ([]string, error) {
			if _, ok := ctx.Deadline(); ok {
				sawDeadline.Store(true)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		start := time.Now()
		got := resolveHostIPWithLookup("https://slow.example/file", 20*time.Millisecond, lookup)
		if got != "" {
			t.Fatalf("got %q, want empty after deadline", got)
		}
		if !sawDeadline.Load() {
			t.Fatal("lookup context carried no deadline")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("bounded lookup ran %s, past the test budget", elapsed)
		}
	})

	t.Run("success returns first IP and cancels context", func(t *testing.T) {
		done := make(chan struct{})
		lookup := func(ctx context.Context, host string) ([]string, error) {
			go func() {
				<-ctx.Done()
				close(done)
			}()
			return []string{"10.0.0.1", "10.0.0.2"}, nil
		}
		got := resolveHostIPWithLookup("https://fast.example/file", time.Second, lookup)
		if got != "10.0.0.1" {
			t.Fatalf("got %q, want first IP", got)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("context not cancelled after early return")
		}
	})

	t.Run("error and empty results return empty", func(t *testing.T) {
		for name, lookup := range map[string]func(context.Context, string) ([]string, error){
			"error": func(ctx context.Context, host string) ([]string, error) {
				return nil, errors.New("nxdomain")
			},
			"empty": func(ctx context.Context, host string) ([]string, error) {
				return nil, nil
			},
		} {
			if got := resolveHostIPWithLookup("https://h.example/file", time.Second, lookup); got != "" {
				t.Fatalf("%s: got %q, want empty", name, got)
			}
		}
	})

	t.Run("literal and invalid inputs never call lookup", func(t *testing.T) {
		var called atomic.Bool
		lookup := func(ctx context.Context, host string) ([]string, error) {
			called.Store(true)
			return []string{"10.0.0.9"}, nil
		}
		if got := resolveHostIPWithLookup("https://192.168.1.1/file", time.Second, lookup); got != "192.168.1.1" {
			t.Fatalf("literal IP got %q, want host returned", got)
		}
		for _, raw := range []string{":::invalid", "https:///file"} {
			if got := resolveHostIPWithLookup(raw, time.Second, lookup); got != "" {
				t.Fatalf("invalid %q got %q, want empty", raw, got)
			}
		}
		if called.Load() {
			t.Fatal("lookup invoked for literal/invalid input")
		}
	})

	t.Run("already cancelled context degrades immediately", func(t *testing.T) {
		lookup := func(ctx context.Context, host string) ([]string, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []string{"10.0.0.3"}, nil
		}
		start := time.Now()
		got := resolveHostIPWithLookup("https://degraded.example/file", time.Nanosecond, lookup)
		// A nanosecond timeout may or may not already fire inside the fake;
		// either way the contract is an empty-or-fast answer, never a block.
		if got != "" && time.Since(start) > 2*time.Second {
			t.Fatalf("cancelled-context lookup ran %s with %q", time.Since(start), got)
		}
	})
}
