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

func TestPreflightWASMModuleStartupIsolation(t *testing.T) {
	t.Run("_start export with trap is ignored and succeeds", func(t *testing.T) {
		wasm := buildWASMWithStartExportTrap()
		pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
		if err := PreflightWASMModule(context.Background(), pack); err != nil {
			t.Fatalf("PreflightWASMModule() failed on harmless _start export: %v", err)
		}
	})

	t.Run("native wasm start section is rejected", func(t *testing.T) {
		wasm := buildWASMWithStartSection()
		pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
		err := PreflightWASMModule(context.Background(), pack)
		if err == nil {
			t.Fatal("PreflightWASMModule() expected error for wasm start section, got nil")
		}
		if !strings.Contains(err.Error(), "start section") {
			t.Fatalf("error %q does not mention start section", err.Error())
		}
	})

	t.Run("match and extract are not executed during preflight", func(t *testing.T) {
		// Module has unreachable instructions in match and extract
		wasm := buildRunnerFixtureWASM(wasmFixtureConfig{
			abiVersion:     CurrentABIVersion,
			trapMatch:      true,
			trapExtract:    true,
			memoryMinPages: 1,
		})
		pack := testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
		if err := PreflightWASMModule(context.Background(), pack); err != nil {
			t.Fatalf("PreflightWASMModule() should not have called match/extract: %v", err)
		}
	})
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
			name: "incompatible version export signature",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithWrongExportSignature(ABIExportVersion)
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "incompatible signature",
		},
		{
			name: "incompatible alloc export signature",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithWrongExportSignature(ABIExportAlloc)
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "incompatible signature",
		},
		{
			name: "incompatible free export signature",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithWrongExportSignature(ABIExportFree)
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "incompatible signature",
		},
		{
			name: "incompatible match export signature",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithWrongExportSignature(ABIExportMatch)
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "incompatible signature",
		},
		{
			name: "incompatible extract export signature",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithWrongExportSignature(ABIExportExtract)
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "incompatible signature",
		},
		{
			name: "incompatible host import signature",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithWrongImportSignature()
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM, CapabilityHTTPFetch})
			},
			errSubstr: "incompatible signature",
		},
		{
			name: "unknown host import function",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithUnknownHostImport()
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "unexpected import function",
		},
		{
			name: "missing memory export",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithMissingMemoryExport()
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "guest memory export is missing",
		},
		{
			name: "multiple memory exports",
			makePack: func() VerifiedPack {
				wasm := buildWASMWithMultipleMemories()
				return testVerifiedPack(wasm, []Capability{CapabilityParseWASM})
			},
			errSubstr: "memor",
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
			errSubstr: "wasm",
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

func buildWASMWithStartExportTrap() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 0, alloc: 1, free: 2, match: 3, extract: 4}

	// Types: 0: () -> (i32), 1: (i32) -> (i32), 2: (i32, i32) -> (), 3: (i32, i32) -> (i64), 4: () -> ()
	var typeSec []byte
	typeSec = appendU32(typeSec, 5)
	typeSec = appendFunctionType(typeSec, nil, []byte{wasmValueTypeI32})
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32}, []byte{wasmValueTypeI32})
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32, wasmValueTypeI32}, nil)
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32, wasmValueTypeI32}, []byte{wasmValueTypeI64})
	typeSec = appendFunctionType(typeSec, nil, nil) // type 4 for _start
	module = appendSection(module, 1, typeSec)

	// Functions: 5 standard + 1 _start (type 4)
	var funcSec []byte
	funcSec = appendU32(funcSec, 6)
	funcSec = appendU32(funcSec, 0)
	funcSec = appendU32(funcSec, 1)
	funcSec = appendU32(funcSec, 2)
	funcSec = appendU32(funcSec, 3)
	funcSec = appendU32(funcSec, 3)
	funcSec = appendU32(funcSec, 4) // _start function index 5
	module = appendSection(module, 3, funcSec)

	module = appendSection(module, 5, buildMemorySection(1))

	// Exports: memory + 5 standard + _start
	var exportSec []byte
	exportSec = appendU32(exportSec, 7)
	exportSec = appendName(exportSec, abiMemoryExport)
	exportSec = append(exportSec, wasmExternMemory)
	exportSec = appendU32(exportSec, 0)

	exportSec = appendName(exportSec, ABIExportVersion)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.version)

	exportSec = appendName(exportSec, ABIExportAlloc)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.alloc)

	exportSec = appendName(exportSec, ABIExportFree)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.free)

	exportSec = appendName(exportSec, ABIExportMatch)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.match)

	exportSec = appendName(exportSec, ABIExportExtract)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.extract)

	exportSec = appendName(exportSec, "_start")
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, 5) // index 5
	module = appendSection(module, 7, exportSec)

	// Code: 5 standard + 1 trapping _start
	bodies := [][]byte{
		functionBody(i32ConstInstructions(CurrentABIVersion)),
		functionBody(i32ConstInstructions(fixtureInputPtr)),
		functionBody(nil),
		functionBody(matchInstructions(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10)),
		functionBody(extractInstructions(wasmFixtureConfig{abiVersion: CurrentABIVersion}, indexes)),
		functionBody([]byte{wasmOpUnreach}), // _start traps if invoked!
	}
	var codeSec []byte
	codeSec = appendU32(codeSec, uint32(len(bodies)))
	for _, body := range bodies {
		codeSec = appendU32(codeSec, uint32(len(body)))
		codeSec = append(codeSec, body...)
	}
	module = appendSection(module, 10, codeSec)
	module = appendSection(module, 11, buildDataSection(`{"matched":true}`, `{"items":[]}`, nil))

	return module
}

func buildWASMWithStartSection() []byte {
	// Standard valid WASM module, but with a Start section (section ID 8) pointing to function index 0
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 0, alloc: 1, free: 2, match: 3, extract: 4}
	module = appendSection(module, 1, buildTypeSection())
	module = appendSection(module, 3, buildFunctionSection(indexes))
	module = appendSection(module, 5, buildMemorySection(1))
	module = appendSection(module, 7, buildExportSection(nil, indexes))

	// Section 8: Start section (func index 0)
	var startSec []byte
	startSec = appendU32(startSec, 0)
	module = appendSection(module, 8, startSec)

	module = appendSection(module, 10, buildCodeSection(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10, indexes))
	module = appendSection(module, 11, buildDataSection(`{"matched":true}`, `{"items":[]}`, nil))
	return module
}

func buildWASMWithWrongExportSignature(exportName string) []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 0, alloc: 1, free: 2, match: 3, extract: 4}

	// Build custom types where one function has wrong type
	var typeSec []byte
	typeSec = appendU32(typeSec, 5)
	typeSec = appendFunctionType(typeSec, nil, []byte{wasmValueTypeI32})                                        // 0: () -> (i32)
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32}, []byte{wasmValueTypeI32})                   // 1: (i32) -> (i32)
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32, wasmValueTypeI32}, nil)                      // 2: (i32, i32) -> ()
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32, wasmValueTypeI32}, []byte{wasmValueTypeI64}) // 3: (i32, i32) -> (i64)
	typeSec = appendFunctionType(typeSec, nil, []byte{wasmValueTypeI64})                                        // 4: () -> (i64) (bad version)

	module = appendSection(module, 1, typeSec)

	// Functions: Assign the wrong type index for the target export
	var funcSec []byte
	funcSec = appendU32(funcSec, 5)
	if exportName == ABIExportVersion {
		funcSec = appendU32(funcSec, 4) // type 4 instead of 0
	} else {
		funcSec = appendU32(funcSec, 0)
	}
	if exportName == ABIExportAlloc {
		funcSec = appendU32(funcSec, 0) // type 0 instead of 1
	} else {
		funcSec = appendU32(funcSec, 1)
	}
	if exportName == ABIExportFree {
		funcSec = appendU32(funcSec, 0) // type 0 instead of 2
	} else {
		funcSec = appendU32(funcSec, 2)
	}
	if exportName == ABIExportMatch {
		funcSec = appendU32(funcSec, 0) // type 0 instead of 3
	} else {
		funcSec = appendU32(funcSec, 3)
	}
	if exportName == ABIExportExtract {
		funcSec = appendU32(funcSec, 2) // type 2 instead of 3
	} else {
		funcSec = appendU32(funcSec, 3)
	}
	module = appendSection(module, 3, funcSec)

	module = appendSection(module, 5, buildMemorySection(1))
	module = appendSection(module, 7, buildExportSection(nil, indexes))

	// Code bodies with instructions matching assigned types
	var body0, body1, body2, body3, body4 []byte
	if exportName == ABIExportVersion {
		body0 = functionBody(i64ConstInstructions(1))
	} else {
		body0 = functionBody(i32ConstInstructions(1))
	}
	body1 = functionBody(i32ConstInstructions(1024))
	if exportName == ABIExportFree {
		body2 = functionBody(i32ConstInstructions(0))
	} else {
		body2 = functionBody(nil)
	}
	if exportName == ABIExportMatch {
		body3 = functionBody(i32ConstInstructions(0))
	} else {
		body3 = functionBody(i64ConstInstructions(0))
	}
	if exportName == ABIExportExtract {
		body4 = functionBody(nil)
	} else {
		body4 = functionBody(i64ConstInstructions(0))
	}

	bodies := [][]byte{body0, body1, body2, body3, body4}
	var codeSec []byte
	codeSec = appendU32(codeSec, uint32(len(bodies)))
	for _, body := range bodies {
		codeSec = appendU32(codeSec, uint32(len(body)))
		codeSec = append(codeSec, body...)
	}
	module = appendSection(module, 10, codeSec)
	module = appendSection(module, 11, buildDataSection(`{"matched":true}`, `{"items":[]}`, nil))

	return module
}

func buildWASMWithWrongImportSignature() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 1, alloc: 2, free: 3, match: 4, extract: 5}

	var typeSec []byte
	typeSec = appendU32(typeSec, 5)
	typeSec = appendFunctionType(typeSec, nil, []byte{wasmValueTypeI32})
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32}, []byte{wasmValueTypeI32})
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32, wasmValueTypeI32}, nil)
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32, wasmValueTypeI32}, []byte{wasmValueTypeI64})
	typeSec = appendFunctionType(typeSec, []byte{wasmValueTypeI32}, []byte{wasmValueTypeI64}) // type 4: (i32) -> (i64) (bad import)
	module = appendSection(module, 1, typeSec)

	// Import with type index 4 (wrong signature)
	var importSec []byte
	importSec = appendU32(importSec, 1)
	importSec = appendName(importSec, HostImportModule)
	importSec = appendName(importSec, HostImportHTTPFetch)
	importSec = append(importSec, wasmExternFunc)
	importSec = appendU32(importSec, 4) // type 4
	module = appendSection(module, 2, importSec)

	module = appendSection(module, 3, buildFunctionSection(indexes))
	module = appendSection(module, 5, buildMemorySection(1))
	module = appendSection(module, 7, buildExportSection(nil, indexes))
	module = appendSection(module, 10, buildCodeSection(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10, indexes))
	module = appendSection(module, 11, buildDataSection(`{"matched":false}`, `{"items":[]}`, nil))

	return module
}

func buildWASMWithUnknownHostImport() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 1, alloc: 2, free: 3, match: 4, extract: 5}
	module = appendSection(module, 1, buildTypeSection())

	var importSec []byte
	importSec = appendU32(importSec, 1)
	importSec = appendName(importSec, HostImportModule)
	importSec = appendName(importSec, "unknown_operation")
	importSec = append(importSec, wasmExternFunc)
	importSec = appendU32(importSec, 3) // type 3: (i32, i32) -> (i64)
	module = appendSection(module, 2, importSec)

	module = appendSection(module, 3, buildFunctionSection(indexes))
	module = appendSection(module, 5, buildMemorySection(1))
	module = appendSection(module, 7, buildExportSection(nil, indexes))
	module = appendSection(module, 10, buildCodeSection(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10, indexes))
	module = appendSection(module, 11, buildDataSection(`{"matched":false}`, `{"items":[]}`, nil))

	return module
}

func buildWASMWithMissingMemoryExport() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 0, alloc: 1, free: 2, match: 3, extract: 4}
	module = appendSection(module, 1, buildTypeSection())
	module = appendSection(module, 3, buildFunctionSection(indexes))
	module = appendSection(module, 5, buildMemorySection(1))
	// Export section without abiMemoryExport
	module = appendSection(module, 7, buildExportSection(map[string]bool{abiMemoryExport: true}, indexes))
	module = appendSection(module, 10, buildCodeSection(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10, indexes))
	module = appendSection(module, 11, buildDataSection(`{"matched":false}`, `{"items":[]}`, nil))
	return module
}

func buildWASMWithMultipleMemories() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := functionIndexes{version: 0, alloc: 1, free: 2, match: 3, extract: 4}
	module = appendSection(module, 1, buildTypeSection())
	module = appendSection(module, 3, buildFunctionSection(indexes))

	// Two memories in memory section
	var memSec []byte
	memSec = appendU32(memSec, 2)
	memSec = append(memSec, 0x00)
	memSec = appendU32(memSec, 1)
	memSec = append(memSec, 0x00)
	memSec = appendU32(memSec, 1)
	module = appendSection(module, 5, memSec)

	// Export both memories
	var exportSec []byte
	exportSec = appendU32(exportSec, 7)
	exportSec = appendName(exportSec, abiMemoryExport)
	exportSec = append(exportSec, wasmExternMemory)
	exportSec = appendU32(exportSec, 0)

	exportSec = appendName(exportSec, "extra_memory")
	exportSec = append(exportSec, wasmExternMemory)
	exportSec = appendU32(exportSec, 1)

	exportSec = appendName(exportSec, ABIExportVersion)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.version)

	exportSec = appendName(exportSec, ABIExportAlloc)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.alloc)

	exportSec = appendName(exportSec, ABIExportFree)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.free)

	exportSec = appendName(exportSec, ABIExportMatch)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.match)

	exportSec = appendName(exportSec, ABIExportExtract)
	exportSec = append(exportSec, wasmExternFunc)
	exportSec = appendU32(exportSec, indexes.extract)
	module = appendSection(module, 7, exportSec)

	module = appendSection(module, 10, buildCodeSection(wasmFixtureConfig{abiVersion: CurrentABIVersion}, 10, indexes))
	module = appendSection(module, 11, buildDataSection(`{"matched":false}`, `{"items":[]}`, nil))
	return module
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
