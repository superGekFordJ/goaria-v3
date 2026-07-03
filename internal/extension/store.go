package extension

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

type SecretStore struct {
	mu        sync.RWMutex
	secret    string
	pairNonce string
}

func NewSecretStore() *SecretStore {
	return &SecretStore{}
}

// GenerateSecret produces a 32-byte random hex string (64 chars).
func (s *SecretStore) GenerateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// GenerateNonce produces a 16-byte random hex string (32 chars).
func (s *SecretStore) GenerateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
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
	s.secret = secret
}

func (s *SecretStore) GetSecret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secret
}

func (s *SecretStore) ClearSecret() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = ""
}

func (s *SecretStore) SetPairNonce(nonce string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairNonce = nonce
}
