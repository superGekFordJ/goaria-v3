package tasks_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"goaria-v3/internal/extractor"
)

func syntheticRootPrivateAuthRuntimeBundle(t *testing.T) *extractor.PrivateAuthRuntimeBundle {
	t.Helper()
	runtimeRaw := []byte(`{"packs":[{"verified_pack_identity":{"pack_id":"xpk-alpha001","pack_version":"opaque-1","asset_sha256":"` + strings.Repeat("1", 64) + `","manifest_sha256":"` + strings.Repeat("2", 64) + `","payload_sha256":"` + strings.Repeat("3", 64) + `","signature_sha256":"` + strings.Repeat("4", 64) + `","public_key_sha256":"` + strings.Repeat("5", 64) + `"},"store_binding":{"scope":"pack","profile_refs":["apr-alpha001"]},"profiles":[{"profile_ref":"apr-alpha001","kind":"bearer","login":{"url":"https://fixture.invalid/login","allowed_domains":[{"host":"fixture.invalid"}],"timeout_millis":30000,` + appHostAuthRuntimeCallbackJSONForTest() + `}}],"preflight":{"mode":"required","missing":"refresh","expired":"refresh"},"provisioning":{"mode":"webview","profile_refs":["apr-alpha001"]},"materialization":{"profile_refs":["apr-alpha001"]},"normalization":{"reject_crlf":true,"trim_space":true}}]}`)
	envelope := []byte(`{"schema_version":1,"bundle_id":"arb-alpha001","bundle_version":"opaque-1","auth_runtime_private_sha256":"` + sha256HexForAppHostAuthTest(runtimeRaw) + `","runtime":` + string(runtimeRaw) + `}`)
	bundle, err := extractor.NewPrivateAuthRuntimeBundle(envelope, extractor.PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}

	return bundle
}

func sha256HexForAppHostAuthTest(raw []byte) string {
	hash := sha256.Sum256(raw)

	return fmt.Sprintf("%x", hash[:])
}
