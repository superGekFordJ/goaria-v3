package extractor

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type EmbeddedPack struct {
	ManifestJSON []byte
	Payload      []byte
	Signature    []byte
}

type VerifiedPack struct {
	Manifest Manifest
	Payload  []byte
}

func VerifyEmbeddedPack(pack EmbeddedPack, policy TrustPolicy) (VerifiedPack, error) {
	if len(pack.ManifestJSON) == 0 {
		return VerifiedPack{}, errors.New("embedded pack manifest is empty")
	}
	if len(pack.Signature) == 0 {
		return VerifiedPack{}, errors.New("embedded pack signature is empty")
	}
	if len(policy.TrustedPublicKeys) == 0 {
		return VerifiedPack{}, errors.New("trust policy has no trusted public keys")
	}

	manifest, err := decodeManifestStrict(pack.ManifestJSON)
	if err != nil {
		return VerifiedPack{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := ValidateManifest(manifest, policy); err != nil {
		return VerifiedPack{}, fmt.Errorf("validate manifest: %w", err)
	}
	if !payloadHashMatches(pack.Payload, manifest.PayloadSHA256) {
		return VerifiedPack{}, errors.New("payload sha256 does not match manifest")
	}
	if !verifyAnyTrustedKey(policy.TrustedPublicKeys, pack.ManifestJSON, pack.Signature) {
		return VerifiedPack{}, errors.New("embedded pack signature verification failed")
	}

	return VerifiedPack{
		Manifest: normalizeManifest(manifest),
		Payload:  cloneBytes(pack.Payload),
	}, nil
}

func decodeManifestStrict(raw []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, errors.New("manifest contains trailing JSON data")
	}

	return manifest, nil
}

func payloadHashMatches(payload []byte, expected string) bool {
	hash := sha256.Sum256(payload)

	return hex.EncodeToString(hash[:]) == expected
}

func verifyAnyTrustedKey(keys []ed25519.PublicKey, message []byte, signature []byte) bool {
	for _, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(key, message, signature) {
			return true
		}
	}

	return false
}

func cloneVerifiedPack(pack VerifiedPack) VerifiedPack {
	return VerifiedPack{
		Manifest: cloneManifest(pack.Manifest),
		Payload:  cloneBytes(pack.Payload),
	}
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Capabilities = append([]Capability(nil), manifest.Capabilities...)
	manifest.Domains = cloneDomainRules(manifest.Domains)

	return manifest
}

func cloneBytes(bytes []byte) []byte {
	if bytes == nil {
		return nil
	}

	cloned := make([]byte, len(bytes))
	copy(cloned, bytes)

	return cloned
}
