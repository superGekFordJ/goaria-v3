package extractor

import (
	"context"
	"strings"
	"testing"
)

func TestPreflightWASMModuleValid(t *testing.T) {
	pack := testVerifiedPack(validRunnerFixtureWASM(), []Capability{CapabilityParseWASM})
	if err := PreflightWASMModule(context.Background(), pack); err != nil {
		t.Fatalf("PreflightWASMModule() unexpected error: %v", err)
	}
}

func TestPreflightWASMModuleWithApprovedHostImports(t *testing.T) {
	t.Run("http_fetch with capability", func(t *testing.T) {
		wasm := httpFetchImportFixtureWASM(`{"url":"https://example.invalid"}`)
		pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM, CapabilityHTTPFetch})
		if err := PreflightWASMModule(context.Background(), pack); err != nil {
			t.Fatalf("PreflightWASMModule() error: %v", err)
		}
	})

	t.Run("http_fetch without capability", func(t *testing.T) {
		wasm := httpFetchImportFixtureWASM(`{"url":"https://example.invalid"}`)
		pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
		err := PreflightWASMModule(context.Background(), pack)
		if err == nil {
			t.Fatal("PreflightWASMModule() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "without manifest capability") {
			t.Fatalf("PreflightWASMModule() error = %q, want capability error", err.Error())
		}
	})

	t.Run("auth_profile_status with capability", func(t *testing.T) {
		wasm := buildRunnerFixtureWASM(wasmFixtureConfig{
			abiVersion:     CurrentABIVersion,
			memoryMinPages: 1,
			hostImports:    []hostImportFixture{hostImportFixtureAuthProfileStatus},
		})
		pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM, CapabilityAuthProfile})
		if err := PreflightWASMModule(context.Background(), pack); err != nil {
			t.Fatalf("PreflightWASMModule() error: %v", err)
		}
	})

	t.Run("auth_profile_status without capability", func(t *testing.T) {
		wasm := buildRunnerFixtureWASM(wasmFixtureConfig{
			abiVersion:     CurrentABIVersion,
			memoryMinPages: 1,
			hostImports:    []hostImportFixture{hostImportFixtureAuthProfileStatus},
		})
		pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
		err := PreflightWASMModule(context.Background(), pack)
		if err == nil {
			t.Fatal("PreflightWASMModule() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "without manifest capability") {
			t.Fatalf("PreflightWASMModule() error = %q, want capability error", err.Error())
		}
	})
}

func TestPreflightWASMModuleRejections(t *testing.T) {
	tests := []struct {
		name      string
		makePack  func() VerifiedPack
		errSubstr string
	}{
		{
			name: "abi version mismatch",
			makePack: func() VerifiedPack {
				return testVerifiedPack(abiMismatchFixtureWASM(), []Capability{CapabilityParseWASM})
			},
			errSubstr: "abi_version",
		},
		{
			name: "missing alloc export",
			makePack: func() VerifiedPack {
				return testVerifiedPack(missingAllocFixtureWASM(), []Capability{CapabilityParseWASM})
			},
			errSubstr: "goaria_alloc",
		},
		{
			name: "missing match export",
			makePack: func() VerifiedPack {
				wasm := buildRunnerFixtureWASM(wasmFixtureConfig{
					abiVersion:     CurrentABIVersion,
					missingExports: map[string]bool{ABIExportMatch: true},
					memoryMinPages: 1,
				})
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "goaria_match",
		},
		{
			name: "missing extract export",
			makePack: func() VerifiedPack {
				wasm := buildRunnerFixtureWASM(wasmFixtureConfig{
					abiVersion:     CurrentABIVersion,
					missingExports: map[string]bool{ABIExportExtract: true},
					memoryMinPages: 1,
				})
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "goaria_extract",
		},
		{
			name: "missing free export",
			makePack: func() VerifiedPack {
				wasm := buildRunnerFixtureWASM(wasmFixtureConfig{
					abiVersion:     CurrentABIVersion,
					missingExports: map[string]bool{ABIExportFree: true},
					memoryMinPages: 1,
				})
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "goaria_free",
		},
		{
			name: "memory min pages exceeds limit",
			makePack: func() VerifiedPack {
				wasm := memoryOverLimitFixtureWASM()
				pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
				pack.Manifest.ResourceLimits.MaxMemoryPages = 1
				return pack
			},
			errSubstr: "limit",
		},
		{
			name: "invalid empty payload",
			makePack: func() VerifiedPack {
				pack := testVerifiedPack(nil, []Capability{CapabilityParseWASM})
				pack.Payload = []byte{}
				return pack
			},
			errSubstr: "empty",
		},
		{
			name: "corrupted payload wasm",
			makePack: func() VerifiedPack {
				pack := testVerifiedPack([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0xff, 0xff}, []Capability{CapabilityParseWASM})
				return pack
			},
			errSubstr: "compile",
		},
		{
			name: "foreign module import",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithForeignImport()
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "unexpected import module",
		},
		{
			name: "imported memory",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithImportedMemory()
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "memory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pack := tc.makePack()
			err := PreflightWASMModule(context.Background(), pack)
			if err == nil {
				t.Fatalf("PreflightWASMModule() succeeded, want error containing %q", tc.errSubstr)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errSubstr)) {
				t.Fatalf("PreflightWASMModule() error = %q, want substring %q", err.Error(), tc.errSubstr)
			}
		})
	}
}

func buildWASMWithForeignImport() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 1, alloc: 2, free: 3, match: 4, extract: 5}
	module = appendSection(module, 1, buildTypeSection())
	var importSec []byte
	importSec = appendU32(importSec, 1)
	importSec = appendName(importSec, "env")
	importSec = appendName(importSec, "print")
	importSec = append(importSec, wasmExternFunc)
	importSec = appendU32(importSec, 0)
	module = appendSection(module, 2, importSec)
	module = appendSection(module, 3, buildFunctionSection(indexes))
	module = appendSection(module, 5, buildMemorySection(1))
	module = appendSection(module, 7, buildExportSection(nil, indexes))
	module = appendSection(module, 10, buildCodeSection(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10, indexes))
	module = appendSection(module, 11, buildDataSection(`{"matched":false}`, `{"items":[]}`, nil))
	return module
}

func buildWASMWithImportedMemory() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 0, alloc: 1, free: 2, match: 3, extract: 4}
	module = appendSection(module, 1, buildTypeSection())
	var importSec []byte
	importSec = appendU32(importSec, 1)
	importSec = appendName(importSec, "env")
	importSec = appendName(importSec, "memory")
	importSec = append(importSec, wasmExternMemory)
	importSec = append(importSec, 0x00)
	importSec = appendU32(importSec, 1)
	module = appendSection(module, 2, importSec)
	module = appendSection(module, 3, buildFunctionSection(indexes))
	module = appendSection(module, 7, buildExportSection(map[string]bool{abiMemoryExport: true}, indexes))
	module = appendSection(module, 10, buildCodeSection(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10, indexes))
	module = appendSection(module, 11, buildDataSection(`{"matched":false}`, `{"items":[]}`, nil))
	return module
}

func testVerifiedPack(payload []byte, capabilities []Capability) VerifiedPack {
	return VerifiedPack{
		Manifest: Manifest{
			PackID:       "fixture-pack",
			PackVersion:  "1.0.0",
			ABIVersion:   CurrentABIVersion,
			Capabilities: capabilities,
			Domains:      []DomainRule{{Host: "example.invalid"}},
			ResourceLimits: ResourceLimits{
				TimeoutMillis:    5000,
				MaxMemoryPages:   16,
				MaxHostCalls:     32,
				MaxResponseBytes: 1024 * 1024,
				MaxOutputItems:   10,
				MaxOutputBytes:   1024 * 1024,
			},
			PayloadSHA256: sha256HexString(payload),
		},
		Payload: payload,
		Identity: VerifiedPackIdentity{
			PackID:      "fixture-pack",
			PackVersion: "1.0.0",
		},
	}
}
