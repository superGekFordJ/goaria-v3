package extractor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
)

var (
	embeddedReleasePacks             []EmbeddedPack
	embeddedReleaseTrustedPublicKeys []ed25519.PublicKey
	embeddedReleaseRequired          bool
)

type EmbeddedReleaseDispatcherConfig struct {
	AuthResolver       AuthProfileResolver
	HeaderResolver     HeaderProfileResolver
	HostPolicyResolver HostPolicyResolver
	AuthRuntimeBundle  *PrivateAuthRuntimeBundle
	Required           *bool
}

func EmbeddedReleasePackCount() int {
	return len(embeddedReleasePacks)
}

func HasEmbeddedReleasePacks() bool {
	return EmbeddedReleasePackCount() > 0
}

func EmbeddedReleaseRequired() bool {
	return embeddedReleaseRequired
}

func EmbeddedReleasePacks() []EmbeddedPack {
	if len(embeddedReleasePacks) == 0 {
		return nil
	}

	packs := make([]EmbeddedPack, len(embeddedReleasePacks))
	for i, pack := range embeddedReleasePacks {
		packs[i] = EmbeddedPack{
			ManifestJSON: cloneBytes(pack.ManifestJSON),
			Payload:      cloneBytes(pack.Payload),
			Signature:    cloneBytes(pack.Signature),
			AssetSHA256:  pack.AssetSHA256,
		}
	}

	return packs
}

func EmbeddedReleaseTrustedPublicKeys() []ed25519.PublicKey {
	if len(embeddedReleaseTrustedPublicKeys) == 0 {
		return nil
	}

	keys := make([]ed25519.PublicKey, len(embeddedReleaseTrustedPublicKeys))
	for i, key := range embeddedReleaseTrustedPublicKeys {
		keys[i] = cloneBytes(key)
	}

	return keys
}

func cloneEmbeddedPack(pack EmbeddedPack) EmbeddedPack {
	return EmbeddedPack{
		ManifestJSON: cloneBytes(pack.ManifestJSON),
		Payload:      cloneBytes(pack.Payload),
		Signature:    cloneBytes(pack.Signature),
		AssetSHA256:  pack.AssetSHA256,
	}
}

func AcceptedEmbeddedPacks(packs []EmbeddedPack, keys []ed25519.PublicKey, config EmbeddedReleaseDispatcherConfig) ([]EmbeddedPack, error) {
	required := embeddedReleaseRequired
	if config.Required != nil {
		required = *config.Required
	}

	if packs == nil {
		packs = EmbeddedReleasePacks()
	}
	if len(packs) == 0 {
		if required {
			return nil, redactErrorf("embedded extractor release packs are required but none are configured")
		}
		return nil, nil
	}

	policy := DefaultTrustPolicy()
	if keys != nil {
		policy.TrustedPublicKeys = keys
	} else {
		policy.TrustedPublicKeys = EmbeddedReleaseTrustedPublicKeys()
	}

	accepted := make([]EmbeddedPack, 0, len(packs))
	acceptedVerified := make([]VerifiedPack, 0, len(packs))
	rejections := make([]PackRejection, 0)
	seenIDs := make(map[string]struct{}, len(packs))

	for i, pack := range packs {
		verified, err := VerifyEmbeddedPack(pack, policy)
		if err != nil {
			rejections = append(rejections, PackRejection{
				PackID: bestEffortPackID(pack.ManifestJSON, i),
				Reason: err.Error(),
			})
			continue
		}
		if _, duplicate := seenIDs[verified.Identity.PackID]; duplicate {
			rejections = append(rejections, PackRejection{
				PackID: verified.Identity.PackID,
				Reason: "duplicate embedded pack id: " + verified.Identity.PackID,
			})
			continue
		}
		seenIDs[verified.Identity.PackID] = struct{}{}
		accepted = append(accepted, cloneEmbeddedPack(pack))
		acceptedVerified = append(acceptedVerified, cloneVerifiedPack(verified))
	}

	if required && len(rejections) > 0 {
		return nil, redactedError(fmt.Errorf("embedded extractor release pack verification rejected configured packs: %s", summarizePackRejections(rejections)))
	}

	if len(acceptedVerified) == 0 {
		if required {
			return nil, redactedError(fmt.Errorf("all embedded extractor release packs were rejected: %s", summarizePackRejections(rejections)))
		}
		return nil, nil
	}

	if required {
		if err := validateRequiredEmbeddedAliasHostPolicies(acceptedVerified, config.HostPolicyResolver); err != nil {
			return nil, err
		}
		if err := validateRequiredEmbeddedAuthRuntime(acceptedVerified, config.AuthRuntimeBundle); err != nil {
			return nil, err
		}
	}

	return accepted, nil
}

func AcceptedEmbeddedReleasePacks(config EmbeddedReleaseDispatcherConfig) ([]EmbeddedPack, error) {
	return AcceptedEmbeddedPacks(nil, nil, config)
}

func NewEmbeddedReleaseAddTaskDispatcher(config EmbeddedReleaseDispatcherConfig) (*AddTaskDispatcher, error) {
	accepted, err := AcceptedEmbeddedReleasePacks(config)
	if err != nil {
		return nil, err
	}
	if len(accepted) == 0 {
		return nil, nil
	}

	policy := DefaultTrustPolicy()
	policy.TrustedPublicKeys = EmbeddedReleaseTrustedPublicKeys()
	registry, _ := NewRegistryWithHostPolicyResolver(accepted, policy, config.HostPolicyResolver)

	return NewAddTaskDispatcher(AddTaskDispatcherConfig{
		Registry: registry,
		Runner: NewRunnerWithConfig(RunnerConfig{
			HTTPBroker: NewHTTPBroker(HTTPBrokerConfig{
				AuthResolver:       config.AuthResolver,
				HostPolicyResolver: config.HostPolicyResolver,
			}),
			AuthResolver:       config.AuthResolver,
			HostPolicyResolver: config.HostPolicyResolver,
		}),
		AuthResolver:   config.AuthResolver,
		HeaderResolver: config.HeaderResolver,
	}), nil
}

func validateRequiredEmbeddedAliasHostPolicies(packs []VerifiedPack, resolver HostPolicyResolver) error {
	for _, pack := range packs {
		if !isAliasManifest(pack.Manifest) {
			continue
		}
		if _, err := resolveAliasHostPolicy(context.Background(), resolver, pack.Identity, pack.Manifest); err != nil {
			return errors.New("required embedded extractor alias host policy is invalid")
		}
	}

	return nil
}

func validateRequiredEmbeddedAuthRuntime(packs []VerifiedPack, bundle *PrivateAuthRuntimeBundle) error {
	requiredIdentities := make(map[VerifiedPackIdentity]struct{})
	for _, pack := range packs {
		if !ManifestHasCapability(pack.Manifest, CapabilityAuthProfile) {
			continue
		}
		requiredIdentities[pack.Identity] = struct{}{}
	}
	if len(requiredIdentities) == 0 {
		return nil
	}
	if bundle == nil || bundle.PackCount() == 0 {
		return errors.New("required embedded authenticated extractor auth runtime is not configured")
	}

	seenRuntime := make(map[VerifiedPackIdentity]struct{}, bundle.PackCount())
	for _, identity := range bundle.PackIdentities() {
		if _, duplicate := seenRuntime[identity]; duplicate {
			return errors.New("required embedded authenticated extractor auth runtime is not configured")
		}
		seenRuntime[identity] = struct{}{}
		if _, ok := requiredIdentities[identity]; !ok {
			return errors.New("required embedded authenticated extractor auth runtime is not configured")
		}
		pack, ok := bundle.PackRuntime(identity)
		if !ok || len(pack.StoreBinding.ProfileRefs) == 0 || len(pack.Materialization.ProfileRefs) == 0 {
			return errors.New("required embedded authenticated extractor auth runtime is not configured")
		}
	}

	if len(seenRuntime) != len(requiredIdentities) {
		return errors.New("required embedded authenticated extractor auth runtime is not configured")
	}
	for identity := range requiredIdentities {
		if _, ok := seenRuntime[identity]; !ok {
			return errors.New("required embedded authenticated extractor auth runtime is not configured")
		}
	}

	return nil
}

func summarizePackRejections(rejections []PackRejection) string {
	if len(rejections) == 0 {
		return "no verified packs"
	}

	parts := make([]string, 0, len(rejections))
	for _, rejection := range rejections {
		packID := rejection.PackID
		if packID == "" {
			packID = "unknown"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", packID, RedactSensitive(rejection.Reason)))
	}

	return strings.Join(parts, "; ")
}
