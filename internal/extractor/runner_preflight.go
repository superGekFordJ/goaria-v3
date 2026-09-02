package extractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// PreflightWASMModule performs a strict, no-network, no-execution static and ABI check on the
// WASM payload of a VerifiedPack before candidate admission.
func PreflightWASMModule(ctx context.Context, pack VerifiedPack) error {
	if err := validateRunnablePack(pack); err != nil {
		return err
	}

	hasStart, err := hasWASMStartSection(pack.Payload)
	if err != nil {
		return fmt.Errorf("check wasm start section: %w", err)
	}
	if hasStart {
		return errors.New("wasm module contains forbidden start section")
	}

	preflightCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	runtimeConfig := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pack.Manifest.ResourceLimits.MaxMemoryPages).
		WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(preflightCtx, runtimeConfig)
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = runtime.Close(closeCtx)
	}()

	compiled, err := runtime.CompileModule(preflightCtx, pack.Payload)
	if err != nil {
		return fmt.Errorf("compile wasm module: %w", err)
	}

	// Validate imported memories (must be 0)
	if len(compiled.ImportedMemories()) > 0 {
		return errors.New("wasm module imports memory")
	}

	// Validate exported memories
	exportedMems := compiled.ExportedMemories()
	memDef, hasMem := exportedMems[abiMemoryExport]
	if !hasMem || memDef == nil {
		return errors.New("guest memory export is missing")
	}
	if len(exportedMems) != 1 {
		return errors.New("wasm module exports multiple memories")
	}
	if memDef.Min() > pack.Manifest.ResourceLimits.MaxMemoryPages {
		return fmt.Errorf("wasm memory min pages %d exceeds manifest limit %d", memDef.Min(), pack.Manifest.ResourceLimits.MaxMemoryPages)
	}
	if maxPages, hasMax := memDef.Max(); hasMax && maxPages > pack.Manifest.ResourceLimits.MaxMemoryPages {
		return fmt.Errorf("wasm memory max pages %d exceeds manifest limit %d", maxPages, pack.Manifest.ResourceLimits.MaxMemoryPages)
	}

	// Validate exported functions
	exportedFns := compiled.ExportedFunctions()
	requiredExports := []struct {
		name    string
		params  []api.ValueType
		results []api.ValueType
	}{
		{
			name:    ABIExportVersion,
			params:  nil,
			results: []api.ValueType{api.ValueTypeI32},
		},
		{
			name:    ABIExportAlloc,
			params:  []api.ValueType{api.ValueTypeI32},
			results: []api.ValueType{api.ValueTypeI32},
		},
		{
			name:    ABIExportFree,
			params:  []api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			results: nil,
		},
		{
			name:    ABIExportMatch,
			params:  []api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			results: []api.ValueType{api.ValueTypeI64},
		},
		{
			name:    ABIExportExtract,
			params:  []api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			results: []api.ValueType{api.ValueTypeI64},
		},
	}
	for _, req := range requiredExports {
		fnDef, ok := exportedFns[req.name]
		if !ok || fnDef == nil {
			return fmt.Errorf("guest function export %q is missing", req.name)
		}
		if !valueTypesEqual(fnDef.ParamTypes(), req.params) || !valueTypesEqual(fnDef.ResultTypes(), req.results) {
			return fmt.Errorf("guest function export %q has incompatible signature", req.name)
		}
	}

	// Validate imported functions
	importedFns := compiled.ImportedFunctions()
	for _, fnDef := range importedFns {
		modName, fnName, isImport := fnDef.Import()
		if !isImport {
			return errors.New("invalid non-import function in imported functions list")
		}
		if modName != HostImportModule {
			return fmt.Errorf("unexpected import module %q", modName)
		}
		expectedParams := []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}
		expectedResults := []api.ValueType{api.ValueTypeI64}
		if !valueTypesEqual(fnDef.ParamTypes(), expectedParams) || !valueTypesEqual(fnDef.ResultTypes(), expectedResults) {
			return fmt.Errorf("import %s.%s has incompatible signature", modName, fnName)
		}
		switch fnName {
		case HostImportHTTPFetch:
			if !manifestHasCapability(pack.Manifest, CapabilityHTTPFetch) {
				return fmt.Errorf("wasm module imports %s without manifest capability %s", HostImportHTTPFetch, CapabilityHTTPFetch)
			}
		case HostImportAuthProfileStatus:
			if !manifestHasCapability(pack.Manifest, CapabilityAuthProfile) {
				return fmt.Errorf("wasm module imports %s without manifest capability %s", HostImportAuthProfileStatus, CapabilityAuthProfile)
			}
		default:
			return fmt.Errorf("unexpected import function %q", fnName)
		}
	}

	// Instantiate inert host module for preflight
	_, err = runtime.NewHostModuleBuilder(HostImportModule).
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		if len(stack) > 0 {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export(HostImportHTTPFetch).
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		if len(stack) > 0 {
			stack[0] = 0
		}
	}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export(HostImportAuthProfileStatus).
		Instantiate(preflightCtx)
	if err != nil {
		return fmt.Errorf("instantiate inert host imports: %w", err)
	}

	// Instantiate guest module without start functions (explicitly disable _start)
	moduleConfig := wazero.NewModuleConfig().WithName("").WithStartFunctions()
	mod, err := runtime.InstantiateModule(preflightCtx, compiled, moduleConfig)
	if err != nil {
		return fmt.Errorf("instantiate wasm module: %w", err)
	}
	defer mod.Close(preflightCtx)

	versionFn := mod.ExportedFunction(ABIExportVersion)
	if versionFn == nil {
		return errors.New("goaria_abi_version export missing")
	}
	results, err := versionFn.Call(preflightCtx)
	if err != nil {
		return fmt.Errorf("call %s: %w", ABIExportVersion, err)
	}
	if len(results) != 1 {
		return fmt.Errorf("%s returned %d results, want 1", ABIExportVersion, len(results))
	}
	version := api.DecodeU32(results[0])
	if version != CurrentABIVersion {
		return fmt.Errorf("guest abi_version %d does not match host abi_version %d", version, CurrentABIVersion)
	}

	return nil
}

func hasWASMStartSection(payload []byte) (bool, error) {
	if len(payload) < 8 {
		return false, errors.New("wasm binary too short")
	}
	if !bytes.Equal(payload[:4], []byte{0x00, 0x61, 0x73, 0x6d}) {
		return false, errors.New("invalid wasm magic")
	}

	offset := 8
	for offset < len(payload) {
		sectionID := payload[offset]
		offset++

		// Decode unsigned LEB128 length
		var size uint32
		var shift uint
		for {
			if offset >= len(payload) {
				return false, errors.New("truncated wasm section header")
			}
			b := payload[offset]
			offset++
			size |= uint32(b&0x7f) << shift
			if b&0x80 == 0 {
				break
			}
			shift += 7
			if shift > 35 {
				return false, errors.New("invalid leb128 in wasm section")
			}
		}

		if sectionID == 8 { // WebAssembly binary Section 8 is the Start section
			return true, nil
		}

		offset += int(size)
		if offset > len(payload) {
			return false, errors.New("truncated wasm section content")
		}
	}

	return false, nil
}
