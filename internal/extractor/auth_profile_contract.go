package extractor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type HostAuthProfileDescriptor struct {
	PackID         string
	ProfileID      AuthProfileID
	AllowedDomains []DomainRule
	StatusCheckURL string
}

type HostAuthProfileStatus struct {
	PackID          string         `json:"pack_id"`
	ProfileID       AuthProfileID  `json:"profile_id"`
	Kind            AuthSecretKind `json:"kind,omitempty"`
	HasSecret       bool           `json:"has_secret"`
	Available       bool           `json:"available"`
	AllowedDomains  []DomainRule   `json:"allowed_domains,omitempty"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	RedactedDisplay string         `json:"redacted_display,omitempty"`
}

func SeedHostAuthProfile(ctx context.Context, store AuthProfileStore, descriptor HostAuthProfileDescriptor, kind AuthSecretKind, secret string, expiresAt *time.Time, redactedDisplay string) (AuthProfileSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return AuthProfileSnapshot{}, redactErrorf("host auth profile store is not configured")
	}
	validated, err := validateHostAuthProfileDescriptor(descriptor)
	if err != nil {
		return AuthProfileSnapshot{}, redactedError(err, secret)
	}
	if err := validateAuthSecretKind(kind); err != nil {
		return AuthProfileSnapshot{}, redactedError(err, secret)
	}

	return store.SetAuthProfile(ctx, AuthProfileUpdate{
		PackID:          validated.PackID,
		ProfileID:       validated.ProfileID,
		Kind:            kind,
		Secret:          secret,
		AllowedDomains:  cloneDomainRules(validated.AllowedDomains),
		ExpiresAt:       expiresAt,
		RedactedDisplay: redactedDisplay,
	})
}

func GetHostAuthProfileStatus(ctx context.Context, store AuthProfileStore, descriptor HostAuthProfileDescriptor) (HostAuthProfileStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	validated, err := validateHostAuthProfileDescriptor(descriptor)
	if err != nil {
		return HostAuthProfileStatus{}, err
	}
	status := HostAuthProfileStatus{
		PackID:         validated.PackID,
		ProfileID:      validated.ProfileID,
		AllowedDomains: cloneDomainRules(validated.AllowedDomains),
	}
	if store == nil {
		return status, nil
	}

	snapshot, ok, err := HostAuthProfileSnapshot(ctx, store, validated)
	if err != nil {
		return HostAuthProfileStatus{}, err
	}
	if ok {
		status.Kind = snapshot.Kind
		status.HasSecret = snapshot.HasSecret
		status.AllowedDomains = cloneDomainRules(snapshot.AllowedDomains)
		status.ExpiresAt = cloneTime(snapshot.ExpiresAt)
		status.RedactedDisplay = snapshot.RedactedDisplay
	}

	resolved, resolveErr := store.ResolveAuthProfile(ctx, validated.PackID, validated.ProfileID, validated.StatusCheckURL)
	if resolveErr == nil {
		status.Available = true
		status.HasSecret = true
		status.Kind = resolved.Kind
		status.RedactedDisplay = RedactSensitive(resolved.RedactedDisplay, authSecretForms(resolved.HeaderName, resolved.HeaderValue)...)
	}

	return status, nil
}

func ClearHostAuthProfile(ctx context.Context, store AuthProfileStore, descriptor HostAuthProfileDescriptor) (HostAuthProfileStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return HostAuthProfileStatus{}, redactErrorf("host auth profile store is not configured")
	}
	validated, err := validateHostAuthProfileDescriptor(descriptor)
	if err != nil {
		return HostAuthProfileStatus{}, err
	}
	if err := store.ClearAuthProfile(ctx, validated.PackID, validated.ProfileID); err != nil {
		return HostAuthProfileStatus{}, err
	}

	return GetHostAuthProfileStatus(ctx, store, validated)
}

func HostAuthProfileSnapshot(ctx context.Context, store AuthProfileStore, descriptor HostAuthProfileDescriptor) (AuthProfileSnapshot, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return AuthProfileSnapshot{}, false, nil
	}
	validated, err := validateHostAuthProfileDescriptor(descriptor)
	if err != nil {
		return AuthProfileSnapshot{}, false, err
	}
	snapshots, err := store.AuthProfileSnapshots(ctx, validated.PackID)
	if err != nil {
		return AuthProfileSnapshot{}, false, err
	}
	for _, snapshot := range snapshots {
		if snapshot.ProfileID == validated.ProfileID {
			return snapshot, true, nil
		}
	}

	return AuthProfileSnapshot{}, false, nil
}

func validateHostAuthProfileDescriptor(descriptor HostAuthProfileDescriptor) (HostAuthProfileDescriptor, error) {
	descriptor.PackID = strings.TrimSpace(descriptor.PackID)
	descriptor.StatusCheckURL = strings.TrimSpace(descriptor.StatusCheckURL)
	descriptor.AllowedDomains = cloneDomainRules(descriptor.AllowedDomains)
	if err := validatePackID(descriptor.PackID); err != nil {
		return HostAuthProfileDescriptor{}, redactedError(fmt.Errorf("host auth descriptor pack_id is invalid: %w", err))
	}
	if err := validateAuthProfileID(descriptor.ProfileID); err != nil {
		return HostAuthProfileDescriptor{}, redactedError(fmt.Errorf("host auth descriptor profile_id is invalid: %w", err))
	}
	if len(descriptor.AllowedDomains) == 0 {
		return HostAuthProfileDescriptor{}, errors.New("host auth descriptor allowed_domains must be non-empty")
	}
	if err := validateDomainRules(descriptor.AllowedDomains); err != nil {
		return HostAuthProfileDescriptor{}, redactedError(fmt.Errorf("host auth descriptor allowed_domains are invalid: %w", err))
	}
	if descriptor.StatusCheckURL == "" {
		return HostAuthProfileDescriptor{}, errors.New("host auth descriptor status check url must be non-empty")
	}
	parsed, host, err := parseSafeHTTPURL(descriptor.StatusCheckURL)
	if err != nil {
		return HostAuthProfileDescriptor{}, redactedError(fmt.Errorf("host auth descriptor status check url is invalid: %w", err))
	}
	if parsed.Scheme != "https" {
		return HostAuthProfileDescriptor{}, redactErrorf("host auth descriptor status check url must use https")
	}
	if !domainRulesMatchHost(descriptor.AllowedDomains, host) {
		return HostAuthProfileDescriptor{}, redactErrorf("host auth descriptor status check url is outside allowed_domains")
	}

	return descriptor, nil
}
