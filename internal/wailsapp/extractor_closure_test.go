package wailsapp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func cleanCommandEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOFLAGS=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "GOFLAGS=")
	return env
}

func TestExtractorDependencyClosure(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Untagged dependency closure: must NOT contain internal/extractor or wazero
	cmdUntagged := exec.CommandContext(ctx, "go", "list", "-deps", ".")
	cmdUntagged.Dir = repoRoot
	cmdUntagged.Env = cleanCommandEnv()
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
	cmdTagged := exec.CommandContext(ctx, "go", "list", "-deps", "-tags", "extractor", ".")
	cmdTagged.Dir = repoRoot
	cmdTagged.Env = cleanCommandEnv()
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

type expectedFieldSpec struct {
	name    string
	typ     reflect.Type
	jsonTag string
}

func assertExactStruct(t *testing.T, typ reflect.Type, expectedFields []expectedFieldSpec) {
	t.Helper()
	if typ.NumField() != len(expectedFields) {
		t.Fatalf("%s expected %d fields, got %d", typ.Name(), len(expectedFields), typ.NumField())
	}
	for i, exp := range expectedFields {
		f := typ.Field(i)
		if f.Name != exp.name {
			t.Errorf("%s field %d name expected %q, got %q", typ.Name(), i, exp.name, f.Name)
		}
		if f.Type != exp.typ {
			t.Errorf("%s field %d type expected %v, got %v", typ.Name(), i, exp.typ, f.Type)
		}
		tag := f.Tag.Get("json")
		if tag != exp.jsonTag {
			t.Errorf("%s field %d json tag expected %q, got %q", typ.Name(), i, exp.jsonTag, tag)
		}
	}
}

func TestExtractorDTOStructuralIdentityAndSignatures(t *testing.T) {
	appType := reflect.TypeFor[*App]()

	expectedMethods := map[string]struct {
		inTypes  []reflect.Type
		outTypes []reflect.Type
	}{
		"GetExtractorState": {
			inTypes:  []reflect.Type{reflect.TypeFor[*App]()},
			outTypes: []reflect.Type{reflect.TypeFor[ExtractorState]()},
		},
		"LoadExtractorPackFile": {
			inTypes:  []reflect.Type{reflect.TypeFor[*App]()},
			outTypes: []reflect.Type{reflect.TypeFor[ExtractorOperationResult]()},
		},
		"LoadExtractorPackDirectory": {
			inTypes:  []reflect.Type{reflect.TypeFor[*App]()},
			outTypes: []reflect.Type{reflect.TypeFor[ExtractorOperationResult]()},
		},
		"LoadExtractorPackURL": {
			inTypes:  []reflect.Type{reflect.TypeFor[*App](), reflect.TypeFor[string]()},
			outTypes: []reflect.Type{reflect.TypeFor[ExtractorOperationResult]()},
		},
		"ReloadExtractorSource": {
			inTypes:  []reflect.Type{reflect.TypeFor[*App](), reflect.TypeFor[string]()},
			outTypes: []reflect.Type{reflect.TypeFor[ExtractorOperationResult]()},
		},
		"RemoveExtractorSource": {
			inTypes:  []reflect.Type{reflect.TypeFor[*App](), reflect.TypeFor[string]()},
			outTypes: []reflect.Type{reflect.TypeFor[ExtractorOperationResult]()},
		},
	}

	for methodName, want := range expectedMethods {
		m, ok := appType.MethodByName(methodName)
		if !ok {
			t.Fatalf("expected App to export method %q", methodName)
		}
		if m.Type.NumIn() != len(want.inTypes) {
			t.Fatalf("method %s expected %d in-params, got %d", methodName, len(want.inTypes), m.Type.NumIn())
		}
		for i, expIn := range want.inTypes {
			if m.Type.In(i) != expIn {
				t.Fatalf("method %s in-param %d expected %v, got %v", methodName, i, expIn, m.Type.In(i))
			}
		}
		if m.Type.NumOut() != len(want.outTypes) {
			t.Fatalf("method %s expected %d out-params, got %d", methodName, len(want.outTypes), m.Type.NumOut())
		}
		for i, expOut := range want.outTypes {
			if m.Type.Out(i) != expOut {
				t.Fatalf("method %s out-param %d expected %v, got %v", methodName, i, expOut, m.Type.Out(i))
			}
		}
	}

	// Verify exact fields and tags for ExtractorState
	assertExactStruct(t, reflect.TypeFor[ExtractorState](), []expectedFieldSpec{
		{name: "Available", typ: reflect.TypeFor[bool](), jsonTag: "available"},
		{name: "Sources", typ: reflect.TypeFor[[]ExtractorSource](), jsonTag: "sources"},
		{name: "RecoveryErrors", typ: reflect.TypeFor[[]string](), jsonTag: "recovery_errors"},
	})

	// Verify exact fields and tags for ExtractorSource
	assertExactStruct(t, reflect.TypeFor[ExtractorSource](), []expectedFieldSpec{
		{name: "SourceID", typ: reflect.TypeFor[string](), jsonTag: "source_id"},
		{name: "Kind", typ: reflect.TypeFor[string](), jsonTag: "kind"},
		{name: "DisplayName", typ: reflect.TypeFor[string](), jsonTag: "display_name"},
		{name: "PackID", typ: reflect.TypeFor[string](), jsonTag: "pack_id"},
		{name: "PackVersion", typ: reflect.TypeFor[string](), jsonTag: "pack_version"},
		{name: "SignerFingerprint", typ: reflect.TypeFor[string](), jsonTag: "signer_fingerprint"},
		{name: "Status", typ: reflect.TypeFor[string](), jsonTag: "status"},
		{name: "ErrorCode", typ: reflect.TypeFor[string](), jsonTag: "error_code,omitempty"},
	})

	// Verify exact fields and tags for ExtractorOperationResult
	assertExactStruct(t, reflect.TypeFor[ExtractorOperationResult](), []expectedFieldSpec{
		{name: "Success", typ: reflect.TypeFor[bool](), jsonTag: "success"},
		{name: "Cancelled", typ: reflect.TypeFor[bool](), jsonTag: "cancelled"},
		{name: "ErrorCode", typ: reflect.TypeFor[string](), jsonTag: "error_code,omitempty"},
		{name: "State", typ: reflect.TypeFor[ExtractorState](), jsonTag: "state"},
	})
}

func TestExtractorBindingsParity(t *testing.T) {
	wailsPath, err := exec.LookPath("wails3")
	if err != nil {
		t.Skip("wails3 CLI not found in PATH; skipping live binding parity generation")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	tempUntagged := t.TempDir()
	tempTagged := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmdUntagged := exec.CommandContext(ctx, wailsPath, "generate", "bindings", "-d", tempUntagged, "-clean=true", "-ts")
	cmdUntagged.Dir = repoRoot
	cmdUntagged.Env = cleanCommandEnv()
	if out, err := cmdUntagged.CombinedOutput(); err != nil {
		t.Fatalf("wails3 generate bindings (untagged) failed: %v\nOutput: %s", err, string(out))
	}

	cmdTagged := exec.CommandContext(ctx, wailsPath, "generate", "bindings", "-f", "-tags extractor", "-d", tempTagged, "-clean=true", "-ts")
	cmdTagged.Dir = repoRoot
	cmdTagged.Env = cleanCommandEnv()
	if out, err := cmdTagged.CombinedOutput(); err != nil {
		t.Fatalf("wails3 generate bindings (tagged) failed: %v\nOutput: %s", err, string(out))
	}

	// Compare generated directory contents byte for byte
	untaggedFiles := make(map[string][]byte)
	err = filepath.Walk(tempUntagged, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(tempUntagged, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		untaggedFiles[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk untagged bindings: %v", err)
	}

	taggedFiles := make(map[string][]byte)
	err = filepath.Walk(tempTagged, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(tempTagged, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		taggedFiles[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk tagged bindings: %v", err)
	}

	if len(untaggedFiles) == 0 {
		t.Fatal("expected non-empty generated bindings")
	}

	for rel, untaggedContent := range untaggedFiles {
		taggedContent, ok := taggedFiles[rel]
		if !ok {
			t.Errorf("file %s present in untagged bindings but missing in tagged bindings", rel)
			continue
		}
		if !bytes.Equal(untaggedContent, taggedContent) {
			t.Errorf("file %s differs between untagged and tagged bindings", rel)
		}
	}

	for rel := range taggedFiles {
		if _, ok := untaggedFiles[rel]; !ok {
			t.Errorf("file %s present in tagged bindings but missing in untagged bindings", rel)
		}
	}
}

func TestExtractorBuildCapabilityMatrix(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// 1. Verify scripts/extractor_tags.sh behavior for accepted and rejected variants
	shellCmd := ""
	for _, candidate := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(candidate); err == nil {
			// Test if shell actually runs
			if err := exec.Command(p, "-c", "exit 0").Run(); err == nil {
				shellCmd = p
				break
			}
		}
	}

	if shellCmd != "" {
		scriptPath := "scripts/extractor_tags.sh"
		cases := []struct {
			variant string
			wantTag string
			wantOk  bool
		}{
			{"", "", true},
			{"generic-no-pack", "", true},
			{"full-pack", "extractor", true},
			{"unknown-variant", "", false},
			{"dev-pack", "", false},
		}

		for _, tc := range cases {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cmd := exec.CommandContext(ctx, shellCmd, scriptPath, tc.variant)
			cmd.Dir = repoRoot
			out, err := cmd.Output()
			cancel()

			if tc.wantOk {
				if err != nil {
					t.Errorf("extractor_tags.sh %q failed: %v", tc.variant, err)
				}
				if string(out) != tc.wantTag {
					t.Errorf("extractor_tags.sh %q output = %q, want %q", tc.variant, string(out), tc.wantTag)
				}
			} else if err == nil {
				t.Errorf("extractor_tags.sh %q succeeded unexpectedly, want non-zero exit", tc.variant)
			}
		}
	}

	// 2. Verify platform Taskfiles propagate extractor capability into DEV flags
	desktopPlatforms := []struct {
		name string
		file string
	}{
		{"windows", filepath.Join(repoRoot, "build", "windows", "Taskfile.yml")},
		{"linux", filepath.Join(repoRoot, "build", "linux", "Taskfile.yml")},
		{"darwin", filepath.Join(repoRoot, "build", "darwin", "Taskfile.yml")},
	}

	for _, p := range desktopPlatforms {
		content, err := os.ReadFile(p.file)
		if err != nil {
			t.Fatalf("read %s: %v", p.file, err)
		}
		text := string(content)
		if !strings.Contains(text, `{{if .EXTRACTOR_BUILD_TAGS}}-tags {{.EXTRACTOR_BUILD_TAGS}}{{end}}`) &&
			!strings.Contains(text, `{{if .EXTRACTOR_BUILD_TAGS}} -tags {{.EXTRACTOR_BUILD_TAGS}}{{end}}`) {
			t.Errorf("%s Taskfile DEV BUILD_FLAGS missing EXTRACTOR_BUILD_TAGS propagation", p.name)
		}
	}

	// iOS DEV flag: -tags ios,debug,extractor when requested
	iosFile := filepath.Join(repoRoot, "build", "ios", "Taskfile.yml")
	iosContent, err := os.ReadFile(iosFile)
	if err != nil {
		t.Fatalf("read ios Taskfile: %v", err)
	}
	if !strings.Contains(string(iosContent), `-tags ios,debug{{if .EXTRACTOR_BUILD_TAGS}},{{.EXTRACTOR_BUILD_TAGS}}{{end}}`) {
		t.Error("ios Taskfile DEV BUILD_FLAGS missing EXTRACTOR_BUILD_TAGS merge into -tags ios,debug")
	}

	// Android DEV flag: -tags android,debug,extractor when requested (build + compile:go:shared)
	androidFile := filepath.Join(repoRoot, "build", "android", "Taskfile.yml")
	androidContent, err := os.ReadFile(androidFile)
	if err != nil {
		t.Fatalf("read android Taskfile: %v", err)
	}
	androidText := string(androidContent)
	expectedAndroidDev := `-tags android,debug{{if .EXTRACTOR_BUILD_TAGS}},{{.EXTRACTOR_BUILD_TAGS}}{{end}}`
	if strings.Count(androidText, expectedAndroidDev) < 2 {
		t.Errorf("android Taskfile missing DEV EXTRACTOR_BUILD_TAGS merge in build or compile:go:shared")
	}

	// 3. Verify build/Taskfile.yml sets wrapper-owned VITE_GOARIA_EXTRACTOR and variant freshness
	commonFile := filepath.Join(repoRoot, "build", "Taskfile.yml")
	commonContent, err := os.ReadFile(commonFile)
	if err != nil {
		t.Fatalf("read common Taskfile: %v", err)
	}
	commonText := string(commonContent)
	if !strings.Contains(commonText, "VITE_GOARIA_EXTRACTOR") {
		t.Error("build/Taskfile.yml missing VITE_GOARIA_EXTRACTOR env definition")
	}
	if !strings.Contains(commonText, "stamp") && !strings.Contains(commonText, "EXTRACTOR_VARIANT") {
		t.Error("build/Taskfile.yml build:frontend missing variant cache invalidation / stamp")
	}
}
