package extractor

import (
	"context"
	"net/http"
	"testing"
)

func TestRunnerRegistersHTTPFetchImport(t *testing.T) {
	request := string(mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://api.fixture.invalid/path"}))
	pack := verifiedRunnerPack(t, httpFetchImportFixtureWASM(request), func(values map[string]any) {
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch)}
	})
	transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "host import ok"}
	runner := NewRunnerWithConfig(RunnerConfig{HTTPBroker: testHTTPBroker(transport, nil)})

	output, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"})
	if err != nil {
		t.Fatalf("Runner.Extract() error = %v", err)
	}
	if len(output.Items) != 0 {
		t.Fatalf("Runner.Extract() items = %#v, want fixture output", output.Items)
	}
	if transport.Count() != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.Count())
	}
	if got := transport.LastRequest().URL.String(); got != "https://api.fixture.invalid/path" {
		t.Fatalf("transport URL = %q, want brokered host request", got)
	}
}

func TestRunnerHostImportMissingCapabilityDoesNotCallTransport(t *testing.T) {
	request := string(mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://api.fixture.invalid/path"}))
	pack := verifiedRunnerPack(t, httpFetchImportFixtureWASM(request), func(values map[string]any) {
		values["capabilities"] = []string{string(CapabilityParseWASM)}
	})
	transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "should not call"}
	runner := NewRunnerWithConfig(RunnerConfig{HTTPBroker: testHTTPBroker(transport, nil)})

	if _, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"}); err != nil {
		t.Fatalf("Runner.Extract() error = %v, want no-panic fixture fallback", err)
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestRunnerHostImportBudgetIsPerOperation(t *testing.T) {
	request := string(mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://api.fixture.invalid/path"}))
	pack := verifiedRunnerPack(t, httpFetchImportFixtureWASM(request), func(values map[string]any) {
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch)}
		limits := values["resource_limits"].(map[string]any)
		limits["max_host_calls"] = 1
	})
	transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "ok"}
	runner := NewRunnerWithConfig(RunnerConfig{HTTPBroker: testHTTPBroker(transport, nil)})

	if _, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"}); err != nil {
		t.Fatalf("first Runner.Extract() error = %v", err)
	}
	if _, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"}); err != nil {
		t.Fatalf("second Runner.Extract() error = %v", err)
	}
	if transport.Count() != 2 {
		t.Fatalf("transport calls after two operations = %d, want 2", transport.Count())
	}

	exhaustingPack := verifiedRunnerPack(t, repeatedHTTPFetchImportFixtureWASM(request, 2), func(values map[string]any) {
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch)}
		limits := values["resource_limits"].(map[string]any)
		limits["max_host_calls"] = 1
	})
	if _, err := runner.Extract(context.Background(), exhaustingPack, ExtractInput{URL: "https://fixture.invalid/d/abc"}); err != nil {
		t.Fatalf("exhausting Runner.Extract() error = %v, want host import fail-closed response only", err)
	}
	if transport.Count() != 3 {
		t.Fatalf("transport calls after exhausted operation = %d, want only one additional privileged call", transport.Count())
	}
}

func TestRunnerNoImportFixturesStillPass(t *testing.T) {
	pack := verifiedRunnerPack(t, validRunnerFixtureWASM(), nil)
	runner := NewRunnerWithConfig(RunnerConfig{})

	match, err := runner.Match(context.Background(), pack, MatchInput{URL: "https://fixture.invalid/d/abc"})
	if err != nil {
		t.Fatalf("Runner.Match() error = %v", err)
	}
	if !match.Matched {
		t.Fatal("Runner.Match() Matched = false, want true")
	}
	extract, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://fixture.invalid/d/abc"})
	if err != nil {
		t.Fatalf("Runner.Extract() error = %v", err)
	}
	if len(extract.Items) != 1 {
		t.Fatalf("Runner.Extract() items = %d, want no-import fixture item", len(extract.Items))
	}
}
