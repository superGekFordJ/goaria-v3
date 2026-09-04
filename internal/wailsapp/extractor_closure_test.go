package wailsapp

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractorDependencyClosure(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// 1. Untagged dependency closure: must NOT contain internal/extractor or wazero
	cmdUntagged := exec.Command("go", "list", "-deps", ".")
	cmdUntagged.Dir = repoRoot
	outUntagged, err := cmdUntagged.Output()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v, output: %s", err, outUntagged)
	}
	untaggedStr := string(outUntagged)
	if strings.Contains(untaggedStr, "goaria-v3/internal/extractor") {
		t.Fatal("untagged dependency closure must not contain goaria-v3/internal/extractor")
	}
	if strings.Contains(untaggedStr, "github.com/tetratelabs/wazero") {
		t.Fatal("untagged dependency closure must not contain github.com/tetratelabs/wazero")
	}

	// 2. Tagged dependency closure: MUST contain internal/extractor and wazero
	cmdTagged := exec.Command("go", "list", "-deps", "-tags", "extractor", ".")
	cmdTagged.Dir = repoRoot
	outTagged, err := cmdTagged.Output()
	if err != nil {
		t.Fatalf("go list -deps -tags extractor . failed: %v, output: %s", err, outTagged)
	}
	taggedStr := string(outTagged)
	if !strings.Contains(taggedStr, "goaria-v3/internal/extractor") {
		t.Fatal("tagged dependency closure must contain goaria-v3/internal/extractor")
	}
	if !strings.Contains(taggedStr, "github.com/tetratelabs/wazero") {
		t.Fatal("tagged dependency closure must contain github.com/tetratelabs/wazero")
	}
}

func TestExtractorDTOStructuralIdentityAndSignatures(t *testing.T) {
	appType := reflect.TypeFor[*App]()

	expectedMethods := map[string]struct {
		inCount  int
		outCount int
	}{
		"GetExtractorState":          {inCount: 1, outCount: 1}, // receiver counts as in[0]
		"LoadExtractorPackFile":      {inCount: 1, outCount: 1},
		"LoadExtractorPackDirectory": {inCount: 1, outCount: 1},
		"LoadExtractorPackURL":       {inCount: 2, outCount: 1},
		"ReloadExtractorSource":      {inCount: 2, outCount: 1},
		"RemoveExtractorSource":      {inCount: 2, outCount: 1},
	}

	for methodName, want := range expectedMethods {
		m, ok := appType.MethodByName(methodName)
		if !ok {
			t.Fatalf("expected App to export method %q", methodName)
		}
		if m.Type.NumIn() != want.inCount {
			t.Fatalf("method %s expected %d in-params, got %d", methodName, want.inCount, m.Type.NumIn())
		}
		if m.Type.NumOut() != want.outCount {
			t.Fatalf("method %s expected %d out-params, got %d", methodName, want.outCount, m.Type.NumOut())
		}
	}

	// Verify ExtractorState fields and json tags
	stateType := reflect.TypeFor[ExtractorState]()
	assertField(t, stateType, "Available", reflect.Bool, "available")
	assertField(t, stateType, "Sources", reflect.Slice, "sources")
	assertField(t, stateType, "RecoveryErrors", reflect.Slice, "recovery_errors")

	// Verify ExtractorSource fields and json tags
	sourceType := reflect.TypeFor[ExtractorSource]()
	assertField(t, sourceType, "SourceID", reflect.String, "source_id")
	assertField(t, sourceType, "Kind", reflect.String, "kind")
	assertField(t, sourceType, "DisplayName", reflect.String, "display_name")
	assertField(t, sourceType, "PackID", reflect.String, "pack_id")
	assertField(t, sourceType, "PackVersion", reflect.String, "pack_version")
	assertField(t, sourceType, "SignerFingerprint", reflect.String, "signer_fingerprint")
	assertField(t, sourceType, "Status", reflect.String, "status")
	assertField(t, sourceType, "ErrorCode", reflect.String, "error_code,omitempty")

	// Verify ExtractorOperationResult fields and json tags
	opResultType := reflect.TypeFor[ExtractorOperationResult]()
	assertField(t, opResultType, "Success", reflect.Bool, "success")
	assertField(t, opResultType, "Cancelled", reflect.Bool, "cancelled")
	assertField(t, opResultType, "ErrorCode", reflect.String, "error_code,omitempty")
	assertField(t, opResultType, "State", reflect.Struct, "state")
}

func assertField(t *testing.T, typ reflect.Type, fieldName string, expectedKind reflect.Kind, expectedJSONTag string) {
	t.Helper()
	f, ok := typ.FieldByName(fieldName)
	if !ok {
		t.Fatalf("type %s missing field %q", typ.Name(), fieldName)
	}
	if f.Type.Kind() != expectedKind {
		t.Fatalf("type %s field %s expected kind %v, got %v", typ.Name(), fieldName, expectedKind, f.Type.Kind())
	}
	tag := f.Tag.Get("json")
	if tag != expectedJSONTag {
		t.Fatalf("type %s field %s expected json tag %q, got %q", typ.Name(), fieldName, expectedJSONTag, tag)
	}
}
