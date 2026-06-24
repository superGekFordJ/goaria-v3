package smartthread

import (
	"sync"
	"testing"
	"time"
)

func TestServerLimitStore_SetGet(t *testing.T) {
	s := NewServerLimitStore()
	s.SetNMax("example.com", 8)

	nMax, ok := s.GetNMax("example.com")
	if !ok {
		t.Fatal("expected NMax to be found")
	}
	if nMax != 8 {
		t.Fatalf("got NMax=%d, want 8", nMax)
	}
}

func TestServerLimitStore_NotFound(t *testing.T) {
	s := NewServerLimitStore()
	_, ok := s.GetNMax("nonexistent.com")
	if ok {
		t.Fatal("expected not found for nonexistent key")
	}
}

func TestServerLimitStore_TTLExpiry(t *testing.T) {
	s := NewServerLimitStore()
	s.mu.Lock()
	s.limits["expired.com"] = &serverLimit{
		NMax:       4,
		DetectedAt: time.Now().Add(-25 * time.Hour),
		TTL:        serverLimitTTL,
	}
	s.mu.Unlock()

	if !s.IsExpired("expired.com") {
		t.Error("expected expired key to be expired")
	}

	_, ok := s.GetNMax("expired.com")
	if ok {
		t.Fatal("expected expired NMax to not be returned")
	}
}

func TestServerLimitStore_Clear(t *testing.T) {
	s := NewServerLimitStore()
	s.SetNMax("clear.com", 16)
	s.Clear("clear.com")

	_, ok := s.GetNMax("clear.com")
	if ok {
		t.Fatal("expected key to be cleared")
	}
}

func TestServerLimitStore_ConcurrentSafe(t *testing.T) {
	s := NewServerLimitStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			s.SetNMax("concurrent.com", n)
		}(i)
		go func() {
			defer wg.Done()
			_, _ = s.GetNMax("concurrent.com")
		}()
	}
	wg.Wait()
}
