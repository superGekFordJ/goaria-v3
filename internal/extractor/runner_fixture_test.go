package extractor

// The fixture helpers below build tiny core-WASM modules directly in Go so
// normal tests do not need TinyGo, wat2wasm, network fetches, or private packs.
// Valid fixture WAT shape:
//
// (module
//   (memory (export "memory") 1)
//   (data (i32.const 2048) "{match json}")
//   (data (i32.const 4096) "{extract json}")
//   (func (export "goaria_abi_version") (result i32) (i32.const 1))
//   (func (export "goaria_alloc") (param i32) (result i32) (i32.const 1024))
//   (func (export "goaria_free") (param i32 i32))
//   (func (export "goaria_match") (param i32 i32) (result i64) (i64.const packed-match-result))
//   (func (export "goaria_extract") (param i32 i32) (result i64) (i64.const packed-extract-result)))

const (
	wasmValueTypeI32 = 0x7f
	wasmValueTypeI64 = 0x7e

	wasmExternFunc   = 0x00
	wasmExternMemory = 0x02

	wasmOpEnd      = 0x0b
	wasmOpCall     = 0x10
	wasmOpDrop     = 0x1a
	wasmOpLoop     = 0x03
	wasmOpBr       = 0x0c
	wasmOpUnreach  = 0x00
	wasmOpI32Const = 0x41
	wasmOpI64Const = 0x42

	fixtureInputPtr   = 1024
	fixtureMatchPtr   = 2048
	fixtureExtractPtr = 4096
	fixtureHostReqPtr = 6144
)

type wasmFixtureConfig struct {
	abiVersion        uint32
	matchJSON         string
	extractJSON       string
	matchOutputLength uint32
	memoryMinPages    uint32
	missingExports    map[string]bool
	timeoutMatch      bool
	trapMatch         bool
	hostImports       []hostImportFixture
	extractHostCalls  []hostImportFixtureCall
}

type hostImportFixture string

const (
	hostImportFixtureHTTPFetch         hostImportFixture = HostImportHTTPFetch
	hostImportFixtureAuthProfileStatus hostImportFixture = HostImportAuthProfileStatus
)

type hostImportFixtureCall struct {
	Name    hostImportFixture
	Request string
	Count   uint32
}

type functionIndexes struct {
	version uint32
	alloc   uint32
	free    uint32
	match   uint32
	extract uint32
	imports map[hostImportFixture]uint32
}

func validRunnerFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true,"confidence":90,"reason":"fixture"}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/file.bin","filename":"file.bin","size_bytes":123,"mime_type":"application/octet-stream","auth_profile_ref":"fixturepack-default","header_profile_ref":"fixturepack-download","metadata":{"source":"fixture"}}]}`,
		memoryMinPages: 1,
	})
}

func abiMismatchFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion + 1,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
	})
}

func missingAllocFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		missingExports: map[string]bool{ABIExportAlloc: true},
	})
}

func timeoutFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		timeoutMatch:   true,
	})
}

func trapFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		trapMatch:      true,
	})
}

func memoryOverLimitFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 2,
	})
}

func outputByteLimitFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:        CurrentABIVersion,
		matchJSON:         `{"matched":true}`,
		extractJSON:       `{"items":[]}`,
		matchOutputLength: 1024,
		memoryMinPages:    1,
	})
}

func outputItemLimitFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/a.bin"},{"url":"https://download.fixture.invalid/b.bin"}]}`,
		memoryMinPages: 1,
	})
}

func secretMetadataFixtureWASM() []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/file.bin","metadata":{"authorization":"redacted"}}]}`,
		memoryMinPages: 1,
	})
}

func httpFetchImportFixtureWASM(request string) []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		hostImports:    []hostImportFixture{hostImportFixtureHTTPFetch},
		extractHostCalls: []hostImportFixtureCall{{
			Name:    hostImportFixtureHTTPFetch,
			Request: request,
			Count:   1,
		}},
	})
}

func repeatedHTTPFetchImportFixtureWASM(request string, count uint32) []byte {
	return buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		hostImports:    []hostImportFixture{hostImportFixtureHTTPFetch},
		extractHostCalls: []hostImportFixtureCall{{
			Name:    hostImportFixtureHTTPFetch,
			Request: request,
			Count:   count,
		}},
	})
}

func buildRunnerFixtureWASM(config wasmFixtureConfig) []byte {
	if config.abiVersion == 0 {
		config.abiVersion = CurrentABIVersion
	}
	if config.memoryMinPages == 0 {
		config.memoryMinPages = 1
	}
	if config.matchJSON == "" {
		config.matchJSON = `{"matched":false}`
	}
	if config.extractJSON == "" {
		config.extractJSON = `{"items":[]}`
	}

	matchLength := uint32(len(config.matchJSON))
	if config.matchOutputLength != 0 {
		matchLength = config.matchOutputLength
	}

	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	indexes := buildFunctionIndexes(config.hostImports)
	module = appendSection(module, 1, buildTypeSection())
	if len(config.hostImports) > 0 {
		module = appendSection(module, 2, buildImportSection(config.hostImports))
	}
	module = appendSection(module, 3, buildFunctionSection(indexes))
	module = appendSection(module, 5, buildMemorySection(config.memoryMinPages))
	module = appendSection(module, 7, buildExportSection(config.missingExports, indexes))
	module = appendSection(module, 10, buildCodeSection(config, matchLength, indexes))
	module = appendSection(module, 11, buildDataSection(config.matchJSON, config.extractJSON, config.extractHostCalls))

	return module
}

func buildTypeSection() []byte {
	var section []byte
	section = appendU32(section, 4)
	section = appendFunctionType(section, nil, []byte{wasmValueTypeI32})
	section = appendFunctionType(section, []byte{wasmValueTypeI32}, []byte{wasmValueTypeI32})
	section = appendFunctionType(section, []byte{wasmValueTypeI32, wasmValueTypeI32}, nil)
	section = appendFunctionType(section, []byte{wasmValueTypeI32, wasmValueTypeI32}, []byte{wasmValueTypeI64})

	return section
}

func appendFunctionType(section []byte, params []byte, results []byte) []byte {
	section = append(section, 0x60)
	section = appendU32(section, uint32(len(params)))
	section = append(section, params...)
	section = appendU32(section, uint32(len(results)))
	section = append(section, results...)

	return section
}

func buildFunctionIndexes(imports []hostImportFixture) functionIndexes {
	indexes := functionIndexes{imports: make(map[hostImportFixture]uint32, len(imports))}
	for i, imported := range imports {
		indexes.imports[imported] = uint32(i)
	}
	base := uint32(len(imports))
	indexes.version = base
	indexes.alloc = base + 1
	indexes.free = base + 2
	indexes.match = base + 3
	indexes.extract = base + 4

	return indexes
}

func buildFunctionSection(indexes functionIndexes) []byte {
	var section []byte
	section = appendU32(section, 5)
	section = appendU32(section, 0)
	section = appendU32(section, 1)
	section = appendU32(section, 2)
	section = appendU32(section, 3)
	section = appendU32(section, 3)

	return section
}

func buildImportSection(imports []hostImportFixture) []byte {
	var section []byte
	section = appendU32(section, uint32(len(imports)))
	for _, imported := range imports {
		section = appendName(section, HostImportModule)
		section = appendName(section, string(imported))
		section = append(section, wasmExternFunc)
		section = appendU32(section, 3)
	}

	return section
}

func buildMemorySection(minPages uint32) []byte {
	var section []byte
	section = appendU32(section, 1)
	section = append(section, 0x00)
	section = appendU32(section, minPages)

	return section
}

func buildExportSection(missing map[string]bool, indexes functionIndexes) []byte {
	type wasmExport struct {
		name  string
		kind  byte
		index uint32
	}

	exports := []wasmExport{
		{name: abiMemoryExport, kind: wasmExternMemory, index: 0},
		{name: ABIExportVersion, kind: wasmExternFunc, index: indexes.version},
		{name: ABIExportAlloc, kind: wasmExternFunc, index: indexes.alloc},
		{name: ABIExportFree, kind: wasmExternFunc, index: indexes.free},
		{name: ABIExportMatch, kind: wasmExternFunc, index: indexes.match},
		{name: ABIExportExtract, kind: wasmExternFunc, index: indexes.extract},
	}

	var filtered []wasmExport
	for _, export := range exports {
		if missing != nil && missing[export.name] {
			continue
		}
		filtered = append(filtered, export)
	}

	var section []byte
	section = appendU32(section, uint32(len(filtered)))
	for _, export := range filtered {
		section = appendName(section, export.name)
		section = append(section, export.kind)
		section = appendU32(section, export.index)
	}

	return section
}

func buildCodeSection(config wasmFixtureConfig, matchLength uint32, indexes functionIndexes) []byte {
	bodies := []wasmFunctionBody{
		{instructions: i32ConstInstructions(config.abiVersion)},
		{instructions: i32ConstInstructions(fixtureInputPtr)},
		{},
		{instructions: matchInstructions(config, matchLength)},
		{instructions: extractInstructions(config, indexes)},
	}

	var section []byte
	section = appendU32(section, uint32(len(bodies)))
	for _, bodyConfig := range bodies {
		body := functionBody(bodyConfig)
		section = appendU32(section, uint32(len(body)))
		section = append(section, body...)
	}

	return section
}

func extractInstructions(config wasmFixtureConfig, indexes functionIndexes) []byte {
	var instructions []byte
	for _, call := range config.extractHostCalls {
		fnIndex, ok := indexes.imports[call.Name]
		if !ok {
			continue
		}
		count := call.Count
		if count == 0 {
			count = 1
		}
		for i := uint32(0); i < count; i++ {
			instructions = append(instructions, i32ConstInstructions(fixtureHostReqPtr)...)
			instructions = append(instructions, wasmOpI32Const)
			instructions = appendS32(instructions, int32(len(call.Request)))
			instructions = append(instructions, wasmOpCall)
			instructions = appendU32(instructions, fnIndex)
			instructions = append(instructions, wasmOpDrop)
		}
	}
	instructions = append(instructions, i64ConstInstructions(packABIResult(fixtureExtractPtr, uint32(len(config.extractJSON))))...)

	return instructions
}

func matchInstructions(config wasmFixtureConfig, matchLength uint32) []byte {
	if config.trapMatch {
		return []byte{wasmOpUnreach, wasmOpI64Const, 0x00}
	}
	if config.timeoutMatch {
		return []byte{
			wasmOpLoop, 0x40,
			wasmOpBr, 0x00,
			wasmOpEnd,
			wasmOpI64Const, 0x00,
		}
	}

	return i64ConstInstructions(packABIResult(fixtureMatchPtr, matchLength))
}

type wasmFunctionBody struct {
	instructions []byte
}

func functionBody(config wasmFunctionBody) []byte {
	body := []byte{0x00}
	body = append(body, config.instructions...)
	body = append(body, wasmOpEnd)

	return body
}

func i32ConstInstructions(value uint32) []byte {
	instructions := []byte{wasmOpI32Const}
	instructions = appendU32(instructions, value)

	return instructions
}

func i64ConstInstructions(value uint64) []byte {
	instructions := []byte{wasmOpI64Const}
	instructions = appendU64(instructions, value)

	return instructions
}

func buildDataSection(matchJSON string, extractJSON string, hostCalls []hostImportFixtureCall) []byte {
	var section []byte
	segmentCount := uint32(2)
	if len(hostCalls) > 0 {
		segmentCount++
	}
	section = appendU32(section, segmentCount)
	section = appendDataSegment(section, fixtureMatchPtr, []byte(matchJSON))
	section = appendDataSegment(section, fixtureExtractPtr, []byte(extractJSON))
	if len(hostCalls) > 0 {
		section = appendDataSegment(section, fixtureHostReqPtr, []byte(hostCalls[0].Request))
	}

	return section
}

func appendDataSegment(section []byte, offset uint32, data []byte) []byte {
	section = append(section, 0x00)
	section = append(section, wasmOpI32Const)
	section = appendU32(section, offset)
	section = append(section, wasmOpEnd)
	section = appendU32(section, uint32(len(data)))
	section = append(section, data...)

	return section
}

func appendSection(module []byte, id byte, payload []byte) []byte {
	module = append(module, id)
	module = appendU32(module, uint32(len(payload)))
	module = append(module, payload...)

	return module
}

func appendName(buffer []byte, name string) []byte {
	buffer = appendU32(buffer, uint32(len(name)))
	buffer = append(buffer, name...)

	return buffer
}

func appendU32(buffer []byte, value uint32) []byte {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			buffer = append(buffer, b|0x80)
			continue
		}

		buffer = append(buffer, b)
		return buffer
	}
}

func appendS32(buffer []byte, value int32) []byte {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		signBitSet := b&0x40 != 0
		done := (value == 0 && !signBitSet) || (value == -1 && signBitSet)
		if !done {
			buffer = append(buffer, b|0x80)
			continue
		}

		buffer = append(buffer, b)
		return buffer
	}
}

func appendU64(buffer []byte, value uint64) []byte {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		signBitSet := b&0x40 != 0
		more := (value != 0 || signBitSet) && (value != ^uint64(0) || !signBitSet)
		if more {
			buffer = append(buffer, b|0x80)
			continue
		}

		buffer = append(buffer, b)
		return buffer
	}
}
