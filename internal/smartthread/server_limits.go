package smartthread

import (
	"sync"
	"time"
)

const serverLimitTTL = 24 * time.Hour

type serverLimit struct {
	NMax       int
	DetectedAt time.Time
	TTL        time.Duration
}

type ServerLimitStore struct {
	mu     sync.RWMutex
	limits map[string]*serverLimit
}

func NewServerLimitStore() *ServerLimitStore {
	return &ServerLimitStore{
		limits: make(map[string]*serverLimit),
	}
}

func (s *ServerLimitStore) GetNMax(key string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sl, ok := s.limits[key]
	if !ok || sl == nil {
		return 0, false
	}
	if time.Since(sl.DetectedAt) > sl.TTL {
		return 0, false
	}
	return sl.NMax, true
}

func (s *ServerLimitStore) SetNMax(key string, nMax int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits[key] = &serverLimit{
		NMax:       nMax,
		DetectedAt: time.Now(),
		TTL:        serverLimitTTL,
	}
}

func (s *ServerLimitStore) IsExpired(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sl, ok := s.limits[key]
	if !ok || sl == nil {
		return true
	}
	return time.Since(sl.DetectedAt) > sl.TTL
}

func (s *ServerLimitStore) Clear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.limits, key)
}

var defaultServerLimits = NewServerLimitStore()

func GetDefaultServerLimits() *ServerLimitStore {
	return defaultServerLimits
}
