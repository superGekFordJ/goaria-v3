package extension

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

// randReader is the rand source for GenerateSecret/GenerateNonce.
// Overridable in tests to simulate rand.Read failure.
var randReader io.Reader = rand.Reader

type SecretStore struct {
	mu         sync.RWMutex
	secret     string
	pairNonce  string
	generation uint64
}

func NewSecretStore() *SecretStore {
	return &SecretStore{}
}

// GenerateSecret produces a 32-byte random hex string (64 chars).
// Retries up to 3 times on rand.Read failure; returns "" on exhaustion.
func (s *SecretStore) GenerateSecret() string {
	for attempt := 0; attempt < 3; attempt++ {
		b := make([]byte, 32)
		if _, err := io.ReadFull(randReader, b); err == nil {
			return hex.EncodeToString(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

// GenerateSecretOrError is the fail-closed variant for production callers.
func (s *SecretStore) GenerateSecretOrError() (string, error) {
	secret := s.GenerateSecret()
	if secret == "" {
		return "", errors.New("failed to generate secret after retries")
	}
	return secret, nil
}

// GenerateNonce produces a 16-byte random hex string (32 chars).
func (s *SecretStore) GenerateNonce() string {
	b := make([]byte, 16)
	if _, err := io.ReadFull(randReader, b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// VerifyAndConsumeNonce checks the nonce matches the stored one and immediately
// clears it (one-time use). Returns true only on a successful match.
func (s *SecretStore) VerifyAndConsumeNonce(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pairNonce == "" || nonce == "" || nonce != s.pairNonce {
		return false
	}
	s.pairNonce = ""
	return true
}

func (s *SecretStore) SetSecret(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if secret != s.secret {
		s.generation++
	}
	s.secret = secret
}

func (s *SecretStore) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

func (s *SecretStore) GetSecret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secret
}

func (s *SecretStore) SetPairNonce(nonce string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairNonce = nonce
}
