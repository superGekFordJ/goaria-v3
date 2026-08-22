package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type AuthProfileID string

type AuthSecretKind string

const (
	AuthSecretKindBearer AuthSecretKind = "bearer"
	AuthSecretKindCookie AuthSecretKind = "cookie"
)

type AuthProfileUpdate struct {
	PackID          string
	ProfileID       AuthProfileID
	Kind            AuthSecretKind
	Secret          string
	AllowedDomains  []DomainRule
	ExpiresAt       *time.Time
	RedactedDisplay string
}

type AuthProfileSnapshot struct {
	PackID          string         `json:"pack_id"`
	ProfileID       AuthProfileID  `json:"profile_id"`
	Kind            AuthSecretKind `json:"kind"`
	HasSecret       bool           `json:"has_secret"`
	AllowedDomains  []DomainRule   `json:"allowed_domains,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	RedactedDisplay string         `json:"redacted_display,omitempty"`
}

func (s AuthProfileSnapshot) String() string {
	return fmt.Sprintf("AuthProfileSnapshot{pack_id:%q profile_id:%q kind:%q has_secret:%t redacted_display:%q}", s.PackID, s.ProfileID, s.Kind, s.HasSecret, s.RedactedDisplay)
}

func (s AuthProfileSnapshot) GoString() string {
	return s.String()
}

type ResolvedAuthSecret struct {
	HeaderName      string
	HeaderValue     string
	Kind            AuthSecretKind
	RedactedDisplay string
}

type AuthProfileResolver interface {
	ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error)
}

type AuthProfileStore interface {
	AuthProfileResolver
	SetAuthProfile(ctx context.Context, update AuthProfileUpdate) (AuthProfileSnapshot, error)
	AuthProfileSnapshots(ctx context.Context, packID string) ([]AuthProfileSnapshot, error)
	ClearAuthProfile(ctx context.Context, packID string, profileID AuthProfileID) error
}

type FileAuthProfileStore struct {
	mu        sync.RWMutex
	path      string
	persistFn func(map[string]authProfileRecord) error
	profiles  map[string]authProfileRecord
}

type authProfileRecord struct {
	PackID          string         `json:"pack_id"`
	ProfileID       AuthProfileID  `json:"profile_id"`
	Kind            AuthSecretKind `json:"kind"`
	Secret          string         `json:"secret"`
	AllowedDomains  []DomainRule   `json:"allowed_domains"`
	UpdatedAt       time.Time      `json:"updated_at"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	RedactedDisplay string         `json:"redacted_display,omitempty"`
}

func (r authProfileRecord) String() string {
	return r.snapshot().String()
}

func (r authProfileRecord) GoString() string {
	return r.String()
}

type authProfileDiskState struct {
	Profiles []authProfileRecord `json:"profiles"`
}

func NewFileAuthProfileStore(path string) (*FileAuthProfileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("auth profile store path must be non-empty")
	}

	store := &FileAuthProfileStore{
		path:     path,
		profiles: make(map[string]authProfileRecord),
	}
	store.persistFn = store.persistToDisk
	if err := store.load(); err != nil {
		return nil, err
	}

	return store, nil
}

func DefaultAuthProfileStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".goaria", "extractor_auth.json"), nil
}

func (s *FileAuthProfileStore) SetAuthProfile(ctx context.Context, update AuthProfileUpdate) (AuthProfileSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return AuthProfileSnapshot{}, redactedError(err, update.Secret)
	}
	record, err := validateAuthProfileUpdate(update)
	if err != nil {
		return AuthProfileSnapshot{}, redactedError(err, update.Secret)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneAuthProfileMap(s.profiles)
	next[authProfileKey(record.PackID, record.ProfileID)] = record
	if err := s.persistFn(next); err != nil {
		return AuthProfileSnapshot{}, redactedError(err, record.Secret)
	}
	s.profiles = next

	return record.snapshot(), nil
}

func (s *FileAuthProfileStore) AuthProfileSnapshots(ctx context.Context, packID string) ([]AuthProfileSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, redactedError(err)
	}
	if err := validatePackID(packID); err != nil {
		return nil, redactedError(err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshots := make([]AuthProfileSnapshot, 0)
	for _, record := range s.profiles {
		if record.PackID != packID {
			continue
		}
		snapshots = append(snapshots, record.snapshot())
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].ProfileID < snapshots[j].ProfileID
	})

	return snapshots, nil
}

func (s *FileAuthProfileStore) ClearAuthProfile(ctx context.Context, packID string, profileID AuthProfileID) error {
	if err := ctx.Err(); err != nil {
		return redactedError(err)
	}
	if err := validatePackID(packID); err != nil {
		return redactedError(err)
	}
	if err := validateAuthProfileID(profileID); err != nil {
		return redactedError(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneAuthProfileMap(s.profiles)
	delete(next, authProfileKey(packID, profileID))
	if err := s.persistFn(next); err != nil {
		return redactedError(err)
	}
	s.profiles = next

	return nil
}

func (s *FileAuthProfileStore) ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error) {
	if err := ctx.Err(); err != nil {
		return ResolvedAuthSecret{}, redactedError(err)
	}
	if err := validatePackID(packID); err != nil {
		return ResolvedAuthSecret{}, redactedError(err)
	}
	if err := validateAuthProfileID(profileID); err != nil {
		return ResolvedAuthSecret{}, redactedError(err)
	}
	_, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return ResolvedAuthSecret{}, err
	}

	s.mu.RLock()
	record, ok := s.profiles[authProfileKey(packID, profileID)]
	s.mu.RUnlock()
	if !ok || record.Secret == "" {
		return ResolvedAuthSecret{}, redactErrorf("auth profile %q for pack %q has no secret", profileID, packID)
	}
	if record.ExpiresAt != nil && time.Now().After(*record.ExpiresAt) {
		return ResolvedAuthSecret{}, redactErrorf("auth profile %q for pack %q is expired", profileID, packID)
	}
	if !domainRulesMatchHost(record.AllowedDomains, host) {
		return ResolvedAuthSecret{}, redactedError(fmt.Errorf("auth profile %q is not allowed for %s", profileID, rawURL), record.Secret)
	}

	headerName, headerValue, err := authHeaderForRecord(record)
	if err != nil {
		return ResolvedAuthSecret{}, redactedError(err, record.Secret)
	}

	return ResolvedAuthSecret{
		HeaderName:      headerName,
		HeaderValue:     headerValue,
		Kind:            record.Kind,
		RedactedDisplay: record.RedactedDisplay,
	}, nil
}

func (s *FileAuthProfileStore) load() error {
	bytes, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read auth profile store: %w", err)
	}
	if len(strings.TrimSpace(string(bytes))) == 0 {
		return nil
	}

	var disk authProfileDiskState
	decoderErr := json.Unmarshal(bytes, &disk)
	if decoderErr != nil {
		return fmt.Errorf("decode auth profile store: %w", decoderErr)
	}
	for _, record := range disk.Profiles {
		normalized, err := normalizeLoadedAuthProfileRecord(record)
		if err != nil {
			return err
		}
		s.profiles[authProfileKey(normalized.PackID, normalized.ProfileID)] = normalized
	}

	return nil
}

func (s *FileAuthProfileStore) persistToDisk(records map[string]authProfileRecord) error {
	profiles := make([]authProfileRecord, 0, len(records))
	for _, record := range records {
		profiles = append(profiles, cloneAuthProfileRecord(record))
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].PackID == profiles[j].PackID {
			return profiles[i].ProfileID < profiles[j].ProfileID
		}

		return profiles[i].PackID < profiles[j].PackID
	})

	bytes, err := json.MarshalIndent(authProfileDiskState{Profiles: profiles}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth profile store: %w", err)
	}
	bytes = append(bytes, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth profile store directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create auth profile temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod auth profile temp file: %w", err)
	}
	if _, err := tmp.Write(bytes); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write auth profile temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod auth profile temp file after write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close auth profile temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace auth profile store: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("chmod auth profile store: %w", err)
	}

	return nil
}

func cloneAuthProfileMap(input map[string]authProfileRecord) map[string]authProfileRecord {
	cloned := make(map[string]authProfileRecord, len(input))
	for key, record := range input {
		cloned[key] = cloneAuthProfileRecord(record)
	}

	return cloned
}

func validateAuthProfileUpdate(update AuthProfileUpdate) (authProfileRecord, error) {
	if err := validatePackID(update.PackID); err != nil {
		return authProfileRecord{}, err
	}
	if err := validateAuthProfileID(update.ProfileID); err != nil {
		return authProfileRecord{}, err
	}
	if err := validateAuthSecretKind(update.Kind); err != nil {
		return authProfileRecord{}, err
	}
	secret := strings.TrimSpace(update.Secret)
	if secret == "" {
		return authProfileRecord{}, errors.New("auth profile secret must be non-empty")
	}
	if strings.ContainsAny(secret, "\r\n") {
		return authProfileRecord{}, errors.New("auth profile secret must not contain CR/LF")
	}
	if err := validateDomainRules(update.AllowedDomains); err != nil {
		return authProfileRecord{}, err
	}

	storedSecret := normalizeStoredSecret(update.Kind, secret)
	redactedDisplay := sanitizedAuthRedactedDisplay(update.Kind, secret, storedSecret, update.RedactedDisplay)

	return authProfileRecord{
		PackID:          update.PackID,
		ProfileID:       update.ProfileID,
		Kind:            update.Kind,
		Secret:          storedSecret,
		AllowedDomains:  cloneDomainRules(update.AllowedDomains),
		UpdatedAt:       time.Now().UTC(),
		ExpiresAt:       cloneTime(update.ExpiresAt),
		RedactedDisplay: redactedDisplay,
	}, nil
}

func normalizeLoadedAuthProfileRecord(record authProfileRecord) (authProfileRecord, error) {
	normalized, err := validateAuthProfileUpdate(AuthProfileUpdate{
		PackID:          record.PackID,
		ProfileID:       record.ProfileID,
		Kind:            record.Kind,
		Secret:          record.Secret,
		AllowedDomains:  record.AllowedDomains,
		ExpiresAt:       record.ExpiresAt,
		RedactedDisplay: record.RedactedDisplay,
	})
	if err != nil {
		return authProfileRecord{}, redactedError(fmt.Errorf("invalid auth profile store record: %w", err), record.Secret)
	}
	normalized.UpdatedAt = record.UpdatedAt
	normalized.ExpiresAt = cloneTime(record.ExpiresAt)

	return normalized, nil
}

func validateAuthProfileID(profileID AuthProfileID) error {
	id := string(profileID)
	if len(id) < 1 || len(id) > 64 {
		return fmt.Errorf("auth profile_id length must be between 1 and 64 characters")
	}
	if !isLowerSlugEdge(id[0]) || !isLowerSlugEdge(id[len(id)-1]) {
		return errors.New("auth profile_id must start and end with a lowercase letter or digit")
	}
	for i := 1; i < len(id)-1; i++ {
		if !isLowerSlugEdge(id[i]) && id[i] != '-' {
			return fmt.Errorf("auth profile_id contains invalid character %q", id[i])
		}
	}

	return nil
}

func validateAuthSecretKind(kind AuthSecretKind) error {
	switch kind {
	case AuthSecretKindBearer, AuthSecretKindCookie:
		return nil
	default:
		return fmt.Errorf("auth secret kind %q is not supported", kind)
	}
}

func normalizeStoredSecret(kind AuthSecretKind, secret string) string {
	if kind == AuthSecretKindBearer && strings.HasPrefix(strings.ToLower(secret), "bearer ") {
		return strings.TrimSpace(secret[len("bearer "):])
	}

	return secret
}

func authHeaderForRecord(record authProfileRecord) (string, string, error) {
	switch record.Kind {
	case AuthSecretKindBearer:
		return "Authorization", "Bearer " + record.Secret, nil
	case AuthSecretKindCookie:
		return "Cookie", record.Secret, nil
	default:
		return "", "", fmt.Errorf("auth secret kind %q is not supported", record.Kind)
	}
}

func (r authProfileRecord) snapshot() AuthProfileSnapshot {
	return AuthProfileSnapshot{
		PackID:          r.PackID,
		ProfileID:       r.ProfileID,
		Kind:            r.Kind,
		HasSecret:       r.Secret != "",
		AllowedDomains:  cloneDomainRules(r.AllowedDomains),
		UpdatedAt:       r.UpdatedAt,
		ExpiresAt:       cloneTime(r.ExpiresAt),
		RedactedDisplay: r.RedactedDisplay,
	}
}

func cloneAuthProfileRecord(record authProfileRecord) authProfileRecord {
	record.AllowedDomains = cloneDomainRules(record.AllowedDomains)
	record.ExpiresAt = cloneTime(record.ExpiresAt)

	return record
}

func cloneTime(input *time.Time) *time.Time {
	if input == nil {
		return nil
	}
	cloned := *input

	return &cloned
}

func authProfileKey(packID string, profileID AuthProfileID) string {
	return packID + "\x00" + string(profileID)
}

func domainRulesMatchHost(rules []DomainRule, host string) bool {
	for _, rule := range rules {
		if matchesDomainRule(host, rule) {
			return true
		}
	}

	return false
}

func redactSecretForDisplay(secret string) string {
	if secret == "" {
		return redactedMarker
	}
	if len(secret) <= 6 {
		return redactedMarker
	}

	return secret[:2] + "…" + secret[len(secret)-2:]
}

func sanitizedAuthRedactedDisplay(kind AuthSecretKind, rawSecret string, storedSecret string, callerDisplay string) string {
	if callerDisplay == "" || displayLeaksSecretForms(kind, rawSecret, storedSecret, callerDisplay) {
		return redactSecretForDisplay(storedSecret)
	}

	return RedactSensitive(callerDisplay, sensitiveSecretForms(kind, rawSecret, storedSecret)...)
}

func displayLeaksSecretForms(kind AuthSecretKind, rawSecret string, storedSecret string, display string) bool {
	redacted := RedactSensitive(display, sensitiveSecretForms(kind, rawSecret, storedSecret)...)

	return redacted != display
}

func sensitiveSecretForms(kind AuthSecretKind, rawSecret string, storedSecret string) []string {
	forms := []string{rawSecret, storedSecret}
	if kind == AuthSecretKindBearer {
		forms = append(forms, "Bearer "+storedSecret)
		if strings.HasPrefix(strings.ToLower(rawSecret), "bearer ") {
			forms = append(forms, strings.TrimSpace(rawSecret[len("bearer "):]))
		}
	}
	if kind == AuthSecretKindCookie {
		for part := range strings.SplitSeq(storedSecret, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			forms = append(forms, part)
			if _, value, ok := strings.Cut(part, "="); ok {
				forms = append(forms, strings.TrimSpace(value))
			}
		}
	}

	return compactUniqueNonEmpty(forms)
}

func compactUniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	compact := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		compact = append(compact, value)
	}

	return compact
}
