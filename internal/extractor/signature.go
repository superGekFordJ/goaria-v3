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
	AssetSHA256  string
}

type VerifiedPackIdentity struct {
	PackID          string
	PackVersion     string
	AssetSHA256     string
	ManifestSHA256  string
	PayloadSHA256   string
	SignatureSHA256 string
	PublicKeySHA256 string
}

type VerifiedPack struct {
	Manifest Manifest
	Payload  []byte
	Identity VerifiedPackIdentity
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
	if pack.AssetSHA256 != "" {
		if err := validateLowerHexSHA256Field("asset_sha256", pack.AssetSHA256); err != nil {
			return VerifiedPack{}, err
		}
	}

	manifestSHA256 := sha256HexString(pack.ManifestJSON)
	payloadSHA256 := sha256HexString(pack.Payload)
	signatureSHA256 := sha256HexString(pack.Signature)

	manifest, err := decodeManifestStrict(pack.ManifestJSON)
	if err != nil {
		return VerifiedPack{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := ValidateManifest(manifest, policy); err != nil {
		return VerifiedPack{}, fmt.Errorf("validate manifest: %w", err)
	}
	if payloadSHA256 != manifest.PayloadSHA256 {
		return VerifiedPack{}, errors.New("payload sha256 does not match manifest")
	}
	publicKeySHA256, ok := verifyAnyTrustedKey(policy.TrustedPublicKeys, pack.ManifestJSON, pack.Signature)
	if !ok {
		return VerifiedPack{}, errors.New("embedded pack signature verification failed")
	}
	normalized := normalizeManifest(manifest)

	return VerifiedPack{
		Manifest: normalized,
		Payload:  cloneBytes(pack.Payload),
		Identity: VerifiedPackIdentity{
			PackID:          normalized.PackID,
			PackVersion:     normalized.PackVersion,
			AssetSHA256:     pack.AssetSHA256,
			ManifestSHA256:  manifestSHA256,
			PayloadSHA256:   payloadSHA256,
			SignatureSHA256: signatureSHA256,
			PublicKeySHA256: publicKeySHA256,
		},
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

func verifyAnyTrustedKey(keys []ed25519.PublicKey, message []byte, signature []byte) (string, bool) {
	for _, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(key, message, signature) {
			return sha256HexString(key), true
		}
	}

	return "", false
}

func cloneVerifiedPack(pack VerifiedPack) VerifiedPack {
	return VerifiedPack{
		Manifest: cloneManifest(pack.Manifest),
		Payload:  cloneBytes(pack.Payload),
		Identity: pack.Identity,
	}
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Capabilities = append([]Capability(nil), manifest.Capabilities...)
	manifest.Domains = cloneDomainRules(manifest.Domains)
	manifest.DomainPolicyRefs = cloneStringSlice(manifest.DomainPolicyRefs)
	manifest.BrokerPolicyRefs = cloneStringSlice(manifest.BrokerPolicyRefs)

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

func sha256HexString(data []byte) string {
	hash := sha256.Sum256(data)

	return hex.EncodeToString(hash[:])
}
