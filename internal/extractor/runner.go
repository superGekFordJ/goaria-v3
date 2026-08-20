package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type Runner struct {
	hostImports HostImportConfig
}

type abiOperation string

const (
	abiOperationMatch   abiOperation = ABIExportMatch
	abiOperationExtract abiOperation = ABIExportExtract
	maxWASMMemoryPages  uint32       = 65_536
)

type abiInvocation struct {
	mod    api.Module
	memory api.Memory
	alloc  api.Function
	free   api.Function
	op     api.Function
}

func NewRunner() *Runner {
	return NewRunnerWithConfig(RunnerConfig{})
}

func NewRunnerWithConfig(config RunnerConfig) *Runner {
	return &Runner{hostImports: HostImportConfig(config)}
}

func (r *Runner) Match(ctx context.Context, pack VerifiedPack, input MatchInput) (MatchOutput, error) {
	if err := ValidateMatchInput(input); err != nil {
		return MatchOutput{}, err
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return MatchOutput{}, fmt.Errorf("encode match input: %w", err)
	}
	outputBytes, err := r.runOperation(ctx, pack, abiOperationMatch, inputBytes)
	if err != nil {
		return MatchOutput{}, err
	}

	output, err := DecodeMatchOutputStrict(outputBytes)
	if err != nil {
		return MatchOutput{}, fmt.Errorf("decode match output: %w", err)
	}
	if err := ValidateMatchOutput(output); err != nil {
		return MatchOutput{}, fmt.Errorf("validate match output: %w", err)
	}

	return output, nil
}

func (r *Runner) Extract(ctx context.Context, pack VerifiedPack, input ExtractInput) (ExtractOutput, error) {
	resetLastHTTPFetchStatus(ctx)
	if err := ValidateExtractInput(input); err != nil {
		return ExtractOutput{}, err
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return ExtractOutput{}, fmt.Errorf("encode extract input: %w", err)
	}
	outputBytes, err := r.runOperation(ctx, pack, abiOperationExtract, inputBytes)
	if err != nil {
		return ExtractOutput{}, err
	}

	output, err := DecodeExtractOutputStrict(outputBytes)
	if err != nil {
		return ExtractOutput{}, fmt.Errorf("decode extract output: %w", err)
	}
	if err := ValidateExtractOutput(output, pack.Manifest.ResourceLimits); err != nil {
		return ExtractOutput{}, fmt.Errorf("validate extract output: %w", err)
	}
	if isAliasManifest(pack.Manifest) && len(output.Items) > 0 {
		policy, err := resolveAliasHostPolicy(ctx, r.hostImports.HostPolicyResolver, pack.Identity, pack.Manifest)
		if err != nil {
			return ExtractOutput{}, fmt.Errorf("validate extract output: %w", err)
		}
		for i, item := range output.Items {
			if err := policyAllowsOutputURL(policy, item.URL); err != nil {
				return ExtractOutput{}, fmt.Errorf("validate extract output: item %d url: %w", i, err)
			}
		}
	}

	return output, nil
}

func (r *Runner) runOperation(ctx context.Context, pack VerifiedPack, operation abiOperation, inputBytes []byte) ([]byte, error) {
	if err := validateRunnablePack(pack); err != nil {
		return nil, err
	}
	if len(inputBytes) == 0 || len(inputBytes) > maxABIInputBytes {
		return nil, fmt.Errorf("abi input length must be between 1 and %d bytes", maxABIInputBytes)
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(pack.Manifest.ResourceLimits.TimeoutMillis)*time.Millisecond)
	defer cancel()

	runtimeConfig := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pack.Manifest.ResourceLimits.MaxMemoryPages).
		WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(execCtx, runtimeConfig)
	defer func() {
		closeCtx := execCtx
		if execCtx.Err() != nil {
			// A timed-out call closes the module; use a fresh context so teardown is best-effort and bounded.
			var closeCancel context.CancelFunc
			closeCtx, closeCancel = context.WithTimeout(context.Background(), time.Second)
			defer closeCancel()
		}
		_ = runtime.Close(closeCtx)
	}()

	budget, err := NewHostCallBudget(pack.Manifest.ResourceLimits.MaxHostCalls)
	if err != nil {
		return nil, err
	}
	bridge := newHostImportBridge(pack, budget, r.hostImports)
	if err := bridge.instantiateHostImports(execCtx, runtime); err != nil {
		return nil, fmt.Errorf("instantiate host imports: %w", err)
	}

	moduleConfig := wazero.NewModuleConfig().WithName("").WithStartFunctions()
	mod, err := runtime.InstantiateWithConfig(execCtx, pack.Payload, moduleConfig)
	if err != nil {
		return nil, fmt.Errorf("instantiate wasm module: %w", err)
	}

	invocation, err := validateGuestExports(mod, operation)
	if err != nil {
		return nil, err
	}
	if err := verifyGuestABIVersion(execCtx, mod); err != nil {
		return nil, err
	}

	inputPtr, err := allocGuestBuffer(execCtx, invocation.alloc, uint32(len(inputBytes)))
	if err != nil {
		return nil, fmt.Errorf("allocate input buffer: %w", err)
	}
	inputAllocated := true
	var primaryErr error
	defer func() {
		if inputAllocated {
			_ = callGuestFree(execCtx, invocation.free, inputPtr, uint32(len(inputBytes)))
		}
	}()

	if !invocation.memory.Write(inputPtr, inputBytes) {
		return nil, errors.New("write input buffer: invalid guest memory range")
	}

	results, err := invocation.op.Call(execCtx, uint64(inputPtr), uint64(uint32(len(inputBytes))))
	if err != nil {
		primaryErr = fmt.Errorf("call %s: %w", operation, err)
		return nil, primaryErr
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("call %s returned %d results, want 1", operation, len(results))
	}

	outputPtr, outputLen := unpackABIResult(results[0])
	if outputLen == 0 {
		return nil, errors.New("guest returned empty output")
	}
	if int64(outputLen) > pack.Manifest.ResourceLimits.MaxOutputBytes {
		return nil, errors.New("guest output exceeds max_output_bytes")
	}
	if outputPtr == 0 {
		return nil, errors.New("guest returned null output pointer")
	}

	outputView, ok := invocation.memory.Read(outputPtr, outputLen)
	if !ok {
		return nil, errors.New("read output buffer: invalid guest memory range")
	}
	outputBytes := cloneBytes(outputView)

	if err := callGuestFree(execCtx, invocation.free, outputPtr, outputLen); err != nil {
		return nil, fmt.Errorf("free output buffer: %w", err)
	}
	if err := callGuestFree(execCtx, invocation.free, inputPtr, uint32(len(inputBytes))); err != nil {
		return nil, fmt.Errorf("free input buffer: %w", err)
	}
	inputAllocated = false

	return outputBytes, primaryErr
}

func validateRunnablePack(pack VerifiedPack) error {
	if pack.Manifest.ABIVersion != CurrentABIVersion {
		return fmt.Errorf("pack abi_version %d does not match host abi_version %d", pack.Manifest.ABIVersion, CurrentABIVersion)
	}
	if !manifestHasCapability(pack.Manifest, CapabilityParseWASM) {
		return errors.New("pack does not declare parse wasm capability")
	}
	if len(pack.Payload) == 0 {
		return errors.New("pack payload is empty")
	}
	if pack.Manifest.ResourceLimits.TimeoutMillis <= 0 {
		return errors.New("timeout_millis must be positive")
	}
	if pack.Manifest.ResourceLimits.MaxMemoryPages == 0 {
		return errors.New("max_memory_pages must be positive")
	}
	if pack.Manifest.ResourceLimits.MaxMemoryPages > maxWASMMemoryPages {
		return errors.New("max_memory_pages exceeds wasm limit")
	}
	if pack.Manifest.ResourceLimits.MaxHostCalls == 0 {
		return errors.New("max_host_calls must be positive")
	}
	if pack.Manifest.ResourceLimits.MaxOutputItems == 0 {
		return errors.New("max_output_items must be positive")
	}
	if pack.Manifest.ResourceLimits.MaxOutputBytes <= 0 {
		return errors.New("max_output_bytes must be positive")
	}

	return nil
}

func manifestHasCapability(manifest Manifest, capability Capability) bool {
	for _, candidate := range manifest.Capabilities {
		if candidate == capability {
			return true
		}
	}

	return false
}

func validateGuestExports(mod api.Module, operation abiOperation) (abiInvocation, error) {
	memory := mod.ExportedMemory(abiMemoryExport)
	if memory == nil {
		return abiInvocation{}, errors.New("guest memory export is missing")
	}
	if mod.Memory() == nil {
		return abiInvocation{}, errors.New("guest default memory is missing")
	}

	alloc, err := requireExportedFunction(mod, ABIExportAlloc, []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32})
	if err != nil {
		return abiInvocation{}, err
	}
	free, err := requireExportedFunction(mod, ABIExportFree, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, nil)
	if err != nil {
		return abiInvocation{}, err
	}
	match, err := requireExportedFunction(mod, ABIExportMatch, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64})
	if err != nil {
		return abiInvocation{}, err
	}
	extract, err := requireExportedFunction(mod, ABIExportExtract, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64})
	if err != nil {
		return abiInvocation{}, err
	}

	op := match
	if operation == abiOperationExtract {
		op = extract
	}

	return abiInvocation{
		mod:    mod,
		memory: memory,
		alloc:  alloc,
		free:   free,
		op:     op,
	}, nil
}

func verifyGuestABIVersion(ctx context.Context, mod api.Module) error {
	versionFn, err := requireExportedFunction(mod, ABIExportVersion, nil, []api.ValueType{api.ValueTypeI32})
	if err != nil {
		return err
	}

	results, err := versionFn.Call(ctx)
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

func requireExportedFunction(mod api.Module, name string, params []api.ValueType, results []api.ValueType) (api.Function, error) {
	fn := mod.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("guest function export %q is missing", name)
	}
	definition := fn.Definition()
	if !valueTypesEqual(definition.ParamTypes(), params) || !valueTypesEqual(definition.ResultTypes(), results) {
		return nil, fmt.Errorf("guest function export %q has incompatible signature", name)
	}

	return fn, nil
}

func valueTypesEqual(actual []api.ValueType, expected []api.ValueType) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false
		}
	}

	return true
}

func allocGuestBuffer(ctx context.Context, alloc api.Function, length uint32) (uint32, error) {
	if length == 0 {
		return 0, errors.New("allocation length must be positive")
	}

	results, err := alloc.Call(ctx, uint64(length))
	if err != nil {
		return 0, err
	}
	if len(results) != 1 {
		return 0, fmt.Errorf("%s returned %d results, want 1", ABIExportAlloc, len(results))
	}

	ptr := api.DecodeU32(results[0])
	if ptr == 0 {
		return 0, errors.New("guest allocation returned null pointer")
	}

	return ptr, nil
}

func callGuestFree(ctx context.Context, free api.Function, ptr, length uint32) error {
	if ptr == 0 || length == 0 {
		return nil
	}

	results, err := free.Call(ctx, uint64(ptr), uint64(length))
	if err != nil {
		return err
	}
	if len(results) != 0 {
		return fmt.Errorf("%s returned %d results, want 0", ABIExportFree, len(results))
	}

	return nil
}
