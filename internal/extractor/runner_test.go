package extractor

import (
	"context"
	"strings"
	"testing"
)

func TestRunnerMatchHappyPath(t *testing.T) {
	pack := verifiedRunnerPack(t, validRunnerFixtureWASM(), nil)

	output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"})
	if err != nil {
		t.Fatalf("Runner.Match() error = %v", err)
	}
	if !output.Matched {
		t.Fatal("Runner.Match() Matched = false, want true")
	}
	if output.Confidence != 90 {
		t.Fatalf("Runner.Match() Confidence = %d, want 90", output.Confidence)
	}
}

func TestRunnerExtractReturnsStructuredItemRefs(t *testing.T) {
	pack := verifiedRunnerPack(t, validRunnerFixtureWASM(), nil)

	output, err := NewRunner().Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"})
	if err != nil {
		t.Fatalf("Runner.Extract() error = %v", err)
	}
	if len(output.Items) != 1 {
		t.Fatalf("Runner.Extract() returned %d items, want 1", len(output.Items))
	}
	item := output.Items[0]
	if item.URL != "https://download.fixture.invalid/file.bin" || item.Filename != "file.bin" {
		t.Fatalf("Runner.Extract() item = %#v, want structured URL/filename", item)
	}
	if item.AuthProfileRef == "" || item.HeaderProfileRef == "" {
		t.Fatalf("Runner.Extract() item missing structured profile refs: %#v", item)
	}
	if _, ok := item.Metadata["authorization"]; ok {
		t.Fatalf("Runner.Extract() accepted raw authorization metadata: %#v", item.Metadata)
	}

	if _, err := DecodeExtractOutputStrict([]byte(`{"items":[{"headers":{"authorization":"secret"}}]}`)); err == nil {
		t.Fatal("DecodeExtractOutputStrict() accepted raw headers field, want error")
	}
}

func TestRunnerRejectsABIMismatch(t *testing.T) {
	pack := verifiedRunnerPack(t, abiMismatchFixtureWASM(), nil)

	if output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Match() error = nil, output = %#v, want error", output)
	}
}

func TestRunnerRejectsMissingRequiredExports(t *testing.T) {
	pack := verifiedRunnerPack(t, missingAllocFixtureWASM(), nil)

	if output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Match() error = nil, output = %#v, want error", output)
	}
}

func TestRunnerEnforcesTimeout(t *testing.T) {
	pack := verifiedRunnerPack(t, timeoutFixtureWASM(), func(values map[string]any) {
		limits := values["resource_limits"].(map[string]any)
		limits["timeout_millis"] = 20
	})

	if output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Match() error = nil, output = %#v, want timeout error", output)
	}
}

func TestRunnerRejectsTrap(t *testing.T) {
	pack := verifiedRunnerPack(t, trapFixtureWASM(), nil)

	if output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Match() error = nil, output = %#v, want trap error", output)
	}
}

func TestRunnerEnforcesMemoryLimit(t *testing.T) {
	pack := verifiedRunnerPack(t, memoryOverLimitFixtureWASM(), func(values map[string]any) {
		limits := values["resource_limits"].(map[string]any)
		limits["max_memory_pages"] = 1
	})

	if output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Match() error = nil, output = %#v, want memory limit error", output)
	}
}

func TestRunnerEnforcesOutputByteLimit(t *testing.T) {
	pack := verifiedRunnerPack(t, outputByteLimitFixtureWASM(), func(values map[string]any) {
		limits := values["resource_limits"].(map[string]any)
		limits["max_output_bytes"] = 32
	})

	if output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Match() error = nil, output = %#v, want output byte cap error", output)
	}
}

func TestRunnerEnforcesOutputItemLimit(t *testing.T) {
	pack := verifiedRunnerPack(t, outputItemLimitFixtureWASM(), func(values map[string]any) {
		limits := values["resource_limits"].(map[string]any)
		limits["max_output_items"] = 1
	})

	if output, err := NewRunner().Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Extract() error = nil, output = %#v, want output item cap error", output)
	}
}

func TestRunnerRejectsRawSecretShapedOutput(t *testing.T) {
	pack := verifiedRunnerPack(t, secretMetadataFixtureWASM(), nil)

	if output, err := NewRunner().Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Extract() error = nil, output = %#v, want secret-shaped output error", output)
	}

	if _, err := DecodeExtractOutputStrict([]byte(`{"items":[{"authorization":"secret"}]}`)); err == nil {
		t.Fatal("DecodeExtractOutputStrict() accepted raw authorization field, want error")
	}
}

func TestHostCallBudgetRejectsOveruse(t *testing.T) {
	budget, err := NewHostCallBudget(2)
	if err != nil {
		t.Fatalf("NewHostCallBudget() error = %v", err)
	}
	if err := budget.Consume(); err != nil {
		t.Fatalf("first Consume() error = %v", err)
	}
	if err := budget.Consume(); err != nil {
		t.Fatalf("second Consume() error = %v", err)
	}
	if err := budget.Consume(); err == nil {
		t.Fatal("third Consume() error = nil, want exhausted error")
	}
	if _, err := NewHostCallBudget(0); err == nil {
		t.Fatal("NewHostCallBudget(0) error = nil, want error")
	}
}

func TestRunnerRequiresVerifiedParseCapability(t *testing.T) {
	pack := verifiedRunnerPack(t, validRunnerFixtureWASM(), nil)
	pack.Manifest.Capabilities = []Capability{CapabilityHTTPFetch}

	if output, err := NewRunner().Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"}); err == nil {
		t.Fatalf("Runner.Match() error = nil, output = %#v, want parse capability error", output)
	}
}

func TestABIValidationRejectsUnsafeInputsAndMetadata(t *testing.T) {
	badInputs := []string{
		"",
		"ftp://fixture.invalid/d/abc",
		"https://user:pass@fixture.invalid/d/abc",
		"https://fixture.invalid/d/abc\r\nheader: value",
	}
	for _, rawURL := range badInputs {
		t.Run("input "+strings.ReplaceAll(rawURL, "\r\n", "_"), func(t *testing.T) {
			if err := ValidateMatchInput(MatchInput{URL: rawURL}); err == nil {
				t.Fatal("ValidateMatchInput() error = nil, want error")
			}
		})
	}

	badOutputs := []ExtractOutput{
		{Items: []ExtractedItemRef{{SizeBytes: -1}}},
		{Items: []ExtractedItemRef{{URL: "https://user:pass@fixture.invalid/file.bin"}}},
		{Items: []ExtractedItemRef{{URL: "file:///tmp/file.bin"}}},
		{Items: []ExtractedItemRef{{Metadata: map[string]string{"x-api-key": "secret"}}}},
	}
	for i, output := range badOutputs {
		t.Run("output", func(t *testing.T) {
			if err := ValidateExtractOutput(output, ResourceLimits{MaxOutputItems: 10}); err == nil {
				t.Fatalf("ValidateExtractOutput(%d) error = nil, want error", i)
			}
		})
	}
}

func TestABIValidationRejectsCredentialShapedMetadataKeyVariants(t *testing.T) {
	credentialKeys := []string{
		"access_token",
		"refresh_token",
		"bearer_token",
		"client_secret",
		"session_cookie",
		"authorization_header",
		"x_auth_token",
		"Access-Token",
		"refresh.token",
		"bearer token",
		"CLIENT_SECRET",
	}

	for _, key := range credentialKeys {
		t.Run(key, func(t *testing.T) {
			output := ExtractOutput{Items: []ExtractedItemRef{{
				URL:      "https://download.fixture.invalid/file.bin",
				Metadata: map[string]string{key: "redacted"},
			}}}
			if err := ValidateExtractOutput(output, ResourceLimits{MaxOutputItems: 10}); err == nil {
				t.Fatal("ValidateExtractOutput() error = nil, want credential-shaped metadata key error")
			}
		})
	}

	safeOutput := ExtractOutput{Items: []ExtractedItemRef{{
		URL:              "https://download.fixture.invalid/file.bin",
		AuthProfileRef:   "fixturepack-default",
		HeaderProfileRef: "fixturepack-download",
		Metadata:         map[string]string{"source": "fixture"},
	}}}
	if err := ValidateExtractOutput(safeOutput, ResourceLimits{MaxOutputItems: 10}); err != nil {
		t.Fatalf("ValidateExtractOutput() rejected safe profile refs: %v", err)
	}
}

func verifiedRunnerPack(t *testing.T, payload []byte, mutate func(map[string]any)) VerifiedPack {
	t.Helper()

	publicKey, privateKey := deterministicKeyPair(42)
	embedded := signedTestPack(t, privateKey, payload, mutate)
	verified, err := VerifyEmbeddedPack(embedded, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}

	return verified
}
