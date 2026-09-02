package extractor_test

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"

	"goaria-v3/internal/extractor"
)

func TestExtractStrictPackZipValid(t *testing.T) {
	manifest := []byte(`{"pack_id":"test-pack"}`)
	payload := []byte{0x00, 0x61, 0x73, 0x6d}
	sig := make([]byte, 64)
	for i := range sig {
		sig[i] = byte(i)
	}

	zipBytes := buildTestZip(t, map[string][]byte{
		"manifest.json": manifest,
		"payload.wasm":  payload,
		"manifest.sig":  sig,
	}, nil)

	archive, err := extractor.ExtractStrictPackZip(zipBytes)
	if err != nil {
		t.Fatalf("ExtractStrictPackZip() unexpected error: %v", err)
	}
	if !bytes.Equal(archive.ManifestJSON, manifest) {
		t.Fatalf("ManifestJSON mismatch")
	}
	if !bytes.Equal(archive.Payload, payload) {
		t.Fatalf("Payload mismatch")
	}
	if !bytes.Equal(archive.Signature, sig) {
		t.Fatalf("Signature mismatch")
	}
}

func TestExtractStrictPackZipRejections(t *testing.T) {
	validManifest := []byte(`{"pack_id":"test-pack"}`)
	validPayload := []byte{0x00, 0x61, 0x73, 0x6d}
	validSig := bytes.Repeat([]byte{0xaa}, 64)

	tests := []struct {
		name      string
		makeZip   func(t *testing.T) []byte
		errSubstr string
	}{
		{
			name: "empty archive bytes",
			makeZip: func(t *testing.T) []byte {
				return []byte{}
			},
			errSubstr: "empty",
		},
		{
			name: "oversize archive bytes",
			makeZip: func(t *testing.T) []byte {
				return make([]byte, extractor.MaxPackAssetBytes+1)
			},
			errSubstr: "exceeds",
		},
		{
			name: "missing manifest.json",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"payload.wasm": validPayload,
					"manifest.sig": validSig,
				}, nil)
			},
			errSubstr: "missing required entries",
		},
		{
			name: "missing payload.wasm",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"manifest.json": validManifest,
					"manifest.sig":  validSig,
				}, nil)
			},
			errSubstr: "missing required entries",
		},
		{
			name: "missing manifest.sig",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"manifest.json": validManifest,
					"payload.wasm":  validPayload,
				}, nil)
			},
			errSubstr: "missing required entries",
		},
		{
			name: "unexpected extra entry",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"manifest.json": validManifest,
					"payload.wasm":  validPayload,
					"manifest.sig":  validSig,
					"extra.txt":     []byte("hello"),
				}, nil)
			},
			errSubstr: "unexpected zip entry",
		},
		{
			name: "duplicate entry",
			makeZip: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				for _, name := range []string{"manifest.json", "payload.wasm", "manifest.sig", "manifest.json"} {
					w, err := zw.Create(name)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = w.Write([]byte("data"))
				}
				_ = zw.Close()
				return buf.Bytes()
			},
			errSubstr: "duplicate zip entry",
		},
		{
			name: "directory entry",
			makeZip: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				_, err := zw.Create("manifest.json/")
				if err != nil {
					t.Fatal(err)
				}
				_ = zw.Close()
				return buf.Bytes()
			},
			errSubstr: "must be a file",
		},
		{
			name: "traversal path prefix",
			makeZip: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				_, err := zw.Create("../manifest.json")
				if err != nil {
					t.Fatal(err)
				}
				_ = zw.Close()
				return buf.Bytes()
			},
			errSubstr: "unsafe path",
		},
		{
			name: "absolute path prefix",
			makeZip: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				_, err := zw.Create("/manifest.json")
				if err != nil {
					t.Fatal(err)
				}
				_ = zw.Close()
				return buf.Bytes()
			},
			errSubstr: "unsafe path",
		},
		{
			name: "backslash in entry name",
			makeZip: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				_, err := zw.Create("sub\\manifest.json")
				if err != nil {
					t.Fatal(err)
				}
				_ = zw.Close()
				return buf.Bytes()
			},
			errSubstr: "unsafe path",
		},
		{
			name: "drive prefix in entry name",
			makeZip: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				_, err := zw.Create("C:manifest.json")
				if err != nil {
					t.Fatal(err)
				}
				_ = zw.Close()
				return buf.Bytes()
			},
			errSubstr: "unsafe path",
		},
		{
			name: "symlink mode entry",
			makeZip: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				h := &zip.FileHeader{
					Name: "manifest.json",
				}
				h.SetMode(os.ModeSymlink | 0o777)
				w, err := zw.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte("target"))
				_ = zw.Close()
				return buf.Bytes()
			},
			errSubstr: "regular file",
		},
		{
			name: "empty entry content",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"manifest.json": {},
					"payload.wasm":  validPayload,
					"manifest.sig":  validSig,
				}, nil)
			},
			errSubstr: "empty",
		},
		{
			name: "manifest exceeds maxManifestBytes",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"manifest.json": make([]byte, extractor.MaxPackManifestBytes+1),
					"payload.wasm":  validPayload,
					"manifest.sig":  validSig,
				}, nil)
			},
			errSubstr: "exceeds",
		},
		{
			name: "manifest header claims small size but decompressed stream exceeds limit",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"manifest.json": bytes.Repeat([]byte("A"), extractor.MaxPackManifestBytes+1024),
					"payload.wasm":  validPayload,
					"manifest.sig":  validSig,
				}, map[string]func(*zip.FileHeader){
					"manifest.json": func(h *zip.FileHeader) {
						h.Method = zip.Deflate
						h.UncompressedSize64 = 100 // forged header claiming small size
					},
				})
			},
			errSubstr: "exceeds",
		},
		{
			name: "payload header uncompressed size exceeds maxPayloadBytes",
			makeZip: func(t *testing.T) []byte {
				base := buildTestZip(t, map[string][]byte{
					"manifest.json": validManifest,
					"payload.wasm":  validPayload,
					"manifest.sig":  validSig,
				}, nil)
				return patchZipEntryUncompressedSize(base, "payload.wasm", uint32(extractor.MaxPackPayloadBytes+1))
			},
			errSubstr: "exceeds",
		},
		{
			name: "signature exceeds maxSignatureBytes",
			makeZip: func(t *testing.T) []byte {
				return buildTestZip(t, map[string][]byte{
					"manifest.json": validManifest,
					"payload.wasm":  validPayload,
					"manifest.sig":  make([]byte, extractor.MaxPackSignatureBytes+1),
				}, nil)
			},
			errSubstr: "exceeds",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			zipData := tc.makeZip(t)
			_, err := extractor.ExtractStrictPackZip(zipData)
			if err == nil {
				t.Fatalf("ExtractStrictPackZip() succeeded, want error containing %q", tc.errSubstr)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errSubstr)) {
				t.Fatalf("ExtractStrictPackZip() error = %q, want substring %q", err.Error(), tc.errSubstr)
			}
		})
	}
}

func buildTestZip(t *testing.T, entries map[string][]byte, headerMod map[string]func(*zip.FileHeader)) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		h := &zip.FileHeader{
			Name:   name,
			Method: zip.Store,
		}
		h.SetMode(0o644)
		if fn, ok := headerMod[name]; ok && fn != nil {
			fn(h)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("create entry header %q: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

func patchZipEntryUncompressedSize(zipBytes []byte, entryName string, size uint32) []byte {
	out := append([]byte(nil), zipBytes...)
	nameBytes := []byte(entryName)
	// Central directory header signature is 0x02014b50 (PK\x01\x02)
	for i := 0; i <= len(out)-46-len(nameBytes); i++ {
		if out[i] == 'P' && out[i+1] == 'K' && out[i+2] == 0x01 && out[i+3] == 0x02 {
			filenameLen := int(out[i+28]) | int(out[i+29])<<8
			if filenameLen == len(nameBytes) && bytes.Equal(out[i+46:i+46+len(nameBytes)], nameBytes) {
				// uncompressed size is at offset 24..27
				out[i+24] = byte(size)
				out[i+25] = byte(size >> 8)
				out[i+26] = byte(size >> 16)
				out[i+27] = byte(size >> 24)
			}
		}
	}
	return out
}
