package packbuilder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packabi"
)

const (
	HostCallFixturePackID    = "hostcall-fixture"
	HostCallFixtureVersion   = "0.1.0-fixture"
	HostCallFixtureAssetName = "hostcall-fixture.pack.zip"

	HostCallFixtureShareURL   = "https://share.fixture.invalid/s/fixture-item"
	HostCallFixtureAPIBaseURL = "https://api.fixture.invalid/resolve"
	HostCallFixtureAPIURL     = HostCallFixtureAPIBaseURL + "/fixture-item"
	HostCallFixtureItemURL    = "https://download.fixture.invalid/artifact.bin"
	HostCallFixtureFilename   = "fixture-artifact.bin"

	HostCallFixtureDomainPolicyRef = "dpr-fixture001"
	HostCallFixtureBrokerPolicyRef = "bpr-fixture001"
	HostCallFixtureEndpointRef     = "ep-fixture001"
)

const (
	wasmValueTypeI32 = 0x7f
	wasmValueTypeI64 = 0x7e

	wasmExternFunc   = 0x00
	wasmExternMemory = 0x02

	wasmOpEnd       = 0x0b
	wasmOpCall      = 0x10
	wasmOpDrop      = 0x1a
	wasmOpI32Load   = 0x28
	wasmOpLocalGet  = 0x20
	wasmOpI32Load8U = 0x2d
	wasmOpI32Add    = 0x6a
	wasmOpI32Sub    = 0x6b
	wasmOpIf        = 0x04
	wasmOpElse      = 0x05
	wasmOpI32Const  = 0x41
	wasmOpI32Eq     = 0x46
	wasmOpI32And    = 0x71
	wasmOpI64Const  = 0x42

	fixtureInputPtr   = 16_384
	fixtureMatchPtr   = 2048
	fixtureExtractPtr = 4096
	fixtureHostReqPtr = 6144
	fixtureNoMatchPtr = 7168
)

const hostCallFixtureRequestJSON = `{"broker_policy_ref":"` + HostCallFixtureBrokerPolicyRef + `","endpoint_ref":"` + HostCallFixtureEndpointRef + `","params":{"id":"fixture-item"}}`

var (
	hostCallFixtureMatchJSON = string(mustFixtureJSON(packabi.MatchOutput{
		Matched:    true,
		Confidence: 90,
		Reason:     "hostcall fixture",
	}))
	hostCallFixtureNoMatchJSON = string(mustFixtureJSON(packabi.MatchOutput{Matched: false}))
	hostCallFixtureExtractJSON = string(mustFixtureJSON(packabi.ExtractOutput{Items: []packabi.ExtractedItemRef{{
		ID:        "fixture-item",
		URL:       HostCallFixtureItemURL,
		Filename:  HostCallFixtureFilename,
		SizeBytes: 1234,
		MimeType:  "application/octet-stream",
		Metadata: map[string]string{
			"source": "hostcall-fixture",
			"broker": "fake",
		},
	}}}))
)

func mustFixtureJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	return raw
}

func BuildHostCallFixturePayload() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	module = appendSection(module, 1, buildTypeSection())
	module = appendSection(module, 2, buildImportSection())
	module = appendSection(module, 3, buildFunctionSection())
	module = appendSection(module, 5, buildMemorySection())
	module = appendSection(module, 7, buildExportSection())
	module = appendSection(module, 10, buildCodeSection())
	module = appendSection(module, 11, buildDataSection())

	return module
}

func HostCallFixtureManifest(payload []byte) extractor.Manifest {
	hash := sha256.Sum256(payload)

	return extractor.Manifest{
		PackID:           HostCallFixturePackID,
		PackVersion:      HostCallFixtureVersion,
		ABIVersion:       packabi.CurrentABIVersion,
		Capabilities:     []extractor.Capability{extractor.CapabilityParseWASM, extractor.CapabilityHTTPFetch},
		Domains:          []extractor.DomainRule{},
		DomainPolicyRefs: []string{HostCallFixtureDomainPolicyRef},
		BrokerPolicyRefs: []string{HostCallFixtureBrokerPolicyRef},
		ResourceLimits: extractor.ResourceLimits{
			TimeoutMillis:    1_000,
			MaxMemoryPages:   1,
			MaxHostCalls:     4,
			MaxResponseBytes: 4 * 1024,
			MaxOutputItems:   4,
			MaxOutputBytes:   8 * 1024,
		},
		PayloadSHA256: hex.EncodeToString(hash[:]),
		Description:   "Public-safe host-call fixture pack for SDK smoke tests.",
	}
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

func buildImportSection() []byte {
	var section []byte
	section = appendU32(section, 1)
	section = appendName(section, packabi.HostImportModule)
	section = appendName(section, packabi.HostImportHTTPFetch)
	section = append(section, wasmExternFunc)
	section = appendU32(section, 3)

	return section
}

func buildFunctionSection() []byte {
	var section []byte
	section = appendU32(section, 5)
	section = appendU32(section, 0)
	section = appendU32(section, 1)
	section = appendU32(section, 2)
	section = appendU32(section, 3)
	section = appendU32(section, 3)

	return section
}

func buildMemorySection() []byte {
	var section []byte
	section = appendU32(section, 1)
	section = append(section, 0x00)
	section = appendU32(section, 1)

	return section
}

func buildExportSection() []byte {
	type wasmExport struct {
		name  string
		kind  byte
		index uint32
	}
	// Function index 0 is the imported host function. Defined functions start at 1.
	exports := []wasmExport{
		{name: "memory", kind: wasmExternMemory, index: 0},
		{name: packabi.ABIExportVersion, kind: wasmExternFunc, index: 1},
		{name: packabi.ABIExportAlloc, kind: wasmExternFunc, index: 2},
		{name: packabi.ABIExportFree, kind: wasmExternFunc, index: 3},
		{name: packabi.ABIExportMatch, kind: wasmExternFunc, index: 4},
		{name: packabi.ABIExportExtract, kind: wasmExternFunc, index: 5},
	}

	var section []byte
	section = appendU32(section, uint32(len(exports)))
	for _, export := range exports {
		section = appendName(section, export.name)
		section = append(section, export.kind)
		section = appendU32(section, export.index)
	}

	return section
}

func buildCodeSection() []byte {
	bodies := [][]byte{
		functionBody(i32ConstInstructions(packabi.CurrentABIVersion)),
		functionBody(i32ConstInstructions(fixtureInputPtr)),
		functionBody(nil),
		functionBody(matchInstructions()),
		functionBody(extractInstructions()),
	}

	var section []byte
	section = appendU32(section, uint32(len(bodies)))
	for _, body := range bodies {
		section = appendU32(section, uint32(len(body)))
		section = append(section, body...)
	}

	return section
}

func extractInstructions() []byte {
	var instructions []byte
	instructions = append(instructions, i32ConstInstructions(fixtureHostReqPtr)...)
	instructions = append(instructions, i32ConstInstructions(uint32(len(hostCallFixtureRequestJSON)))...)
	instructions = append(instructions, wasmOpCall)
	instructions = appendU32(instructions, 0)
	instructions = append(instructions, wasmOpDrop)
	instructions = append(instructions, i64ConstInstructions(packabi.PackResult(fixtureExtractPtr, uint32(len(hostCallFixtureExtractJSON))))...)

	return instructions
}

func matchInstructions() []byte {
	shareURL := []byte(HostCallFixtureShareURL)
	chunkCount := len(shareURL) / 4
	jsonURLPrefixLen := uint32(len(`{"url":"`))
	var instructions []byte
	instructions = append(instructions, wasmOpLocalGet, 0x01)
	instructions = append(instructions, i32ConstInstructions(jsonURLPrefixLen+uint32(len(`"}`)))...)
	instructions = append(instructions, wasmOpI32Sub)
	instructions = append(instructions, i32ConstInstructions(uint32(len(shareURL)))...)
	instructions = append(instructions, wasmOpI32Eq)
	for chunk := range chunkCount {
		offset := chunk * 4
		instructions = append(instructions, wasmOpLocalGet, 0x00)
		instructions = append(instructions, i32ConstInstructions(jsonURLPrefixLen+uint32(offset))...)
		instructions = append(instructions, wasmOpI32Add)
		instructions = append(instructions, wasmOpI32Load)
		instructions = appendU32(instructions, 0)
		instructions = appendU32(instructions, 0)
		value := uint32(shareURL[offset]) | uint32(shareURL[offset+1])<<8 | uint32(shareURL[offset+2])<<16 | uint32(shareURL[offset+3])<<24
		instructions = append(instructions, i32ConstInstructions(value)...)
		instructions = append(instructions, wasmOpI32Eq, wasmOpI32And)
	}
	for offset := chunkCount * 4; offset < len(shareURL); offset++ {
		instructions = append(instructions, wasmOpLocalGet, 0x00)
		instructions = append(instructions, i32ConstInstructions(jsonURLPrefixLen+uint32(offset))...)
		instructions = append(instructions, wasmOpI32Add)
		instructions = append(instructions, wasmOpI32Load8U)
		instructions = appendU32(instructions, 0)
		instructions = appendU32(instructions, 0)
		instructions = append(instructions, i32ConstInstructions(uint32(shareURL[offset]))...)
		instructions = append(instructions, wasmOpI32Eq, wasmOpI32And)
	}
	instructions = append(instructions, wasmOpIf, wasmValueTypeI64)
	instructions = append(instructions, i64ConstInstructions(packabi.PackResult(fixtureMatchPtr, uint32(len(hostCallFixtureMatchJSON))))...)
	instructions = append(instructions, wasmOpElse)
	instructions = append(instructions, i64ConstInstructions(packabi.PackResult(fixtureNoMatchPtr, uint32(len(hostCallFixtureNoMatchJSON))))...)
	instructions = append(instructions, wasmOpEnd)

	return instructions
}

func buildDataSection() []byte {
	var section []byte
	section = appendU32(section, 4)
	section = appendDataSegment(section, fixtureMatchPtr, []byte(hostCallFixtureMatchJSON))
	section = appendDataSegment(section, fixtureExtractPtr, []byte(hostCallFixtureExtractJSON))
	section = appendDataSegment(section, fixtureHostReqPtr, []byte(hostCallFixtureRequestJSON))
	section = appendDataSegment(section, fixtureNoMatchPtr, []byte(hostCallFixtureNoMatchJSON))

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

func functionBody(instructions []byte) []byte {
	body := []byte{0x00}
	body = append(body, instructions...)
	body = append(body, wasmOpEnd)

	return body
}

func i32ConstInstructions(value uint32) []byte {
	instructions := []byte{wasmOpI32Const}
	instructions = appendI32(instructions, int32(value))

	return instructions
}

func i64ConstInstructions(value uint64) []byte {
	instructions := []byte{wasmOpI64Const}
	instructions = appendI64(instructions, int64(value))

	return instructions
}

func appendI32(buffer []byte, value int32) []byte {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		done := value == 0 && b&0x40 == 0 || value == -1 && b&0x40 != 0
		if !done {
			b |= 0x80
		}
		buffer = append(buffer, b)
		if done {
			return buffer
		}
	}
}

func appendI64(buffer []byte, value int64) []byte {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		done := value == 0 && b&0x40 == 0 || value == -1 && b&0x40 != 0
		if !done {
			b |= 0x80
		}
		buffer = append(buffer, b)
		if done {
			return buffer
		}
	}
}

func appendDataSegment(section []byte, offset uint32, data []byte) []byte {
	section = append(section, 0x00)
	section = append(section, i32ConstInstructions(offset)...)
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
