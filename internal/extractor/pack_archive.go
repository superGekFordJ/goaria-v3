package extractor

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

const (
	MaxPackAssetBytes     = 64 * 1024 * 1024
	MaxPackManifestBytes  = 256 * 1024
	MaxPackPayloadBytes   = 32 * 1024 * 1024
	MaxPackSignatureBytes = 64 * 1024
)

type PackArchive struct {
	ManifestJSON []byte
	Payload      []byte
	Signature    []byte
}

func ExtractStrictPackZip(assetBytes []byte) (PackArchive, error) {
	if len(assetBytes) == 0 {
		return PackArchive{}, errors.New("pack zip is empty")
	}
	if int64(len(assetBytes)) > MaxPackAssetBytes {
		return PackArchive{}, fmt.Errorf("pack zip exceeds %d bytes", MaxPackAssetBytes)
	}

	reader, err := zip.NewReader(bytes.NewReader(assetBytes), int64(len(assetBytes)))
	if err != nil {
		return PackArchive{}, fmt.Errorf("open pack zip: %w", err)
	}

	limits := map[string]int64{
		"manifest.json": MaxPackManifestBytes,
		"payload.wasm":  MaxPackPayloadBytes,
		"manifest.sig":  MaxPackSignatureBytes,
	}
	seen := make(map[string][]byte, len(limits))
	for _, file := range reader.File {
		name := file.Name
		if err := validateZipEntryName(name); err != nil {
			return PackArchive{}, err
		}
		limit, ok := limits[name]
		if !ok {
			return PackArchive{}, fmt.Errorf("unexpected zip entry %q", name)
		}
		if _, ok := seen[name]; ok {
			return PackArchive{}, fmt.Errorf("duplicate zip entry %q", name)
		}
		if modeType := file.FileInfo().Mode() & os.ModeType; modeType != 0 {
			return PackArchive{}, fmt.Errorf("zip entry %q must be a regular file", name)
		}
		if file.UncompressedSize64 > uint64(limit) {
			return PackArchive{}, fmt.Errorf("zip entry %q exceeds %d bytes", name, limit)
		}

		entryBytes, err := readZipEntry(file, limit)
		if err != nil {
			return PackArchive{}, err
		}
		if len(entryBytes) == 0 {
			return PackArchive{}, fmt.Errorf("zip entry %q is empty", name)
		}
		seen[name] = entryBytes
	}

	missing := make([]string, 0)
	for name := range limits {
		if _, ok := seen[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return PackArchive{}, fmt.Errorf("pack zip missing required entries: %s", strings.Join(missing, ", "))
	}

	return PackArchive{
		ManifestJSON: seen["manifest.json"],
		Payload:      seen["payload.wasm"],
		Signature:    seen["manifest.sig"],
	}, nil
}

func validateZipEntryName(name string) error {
	if name == "" || strings.HasSuffix(name, "/") {
		return fmt.Errorf("zip entry %q must be a file", name)
	}
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return fmt.Errorf("zip entry %q has an unsafe path", name)
	}
	if path.Clean(name) != name || strings.Contains(name, ":") {
		return fmt.Errorf("zip entry %q has an unsafe path", name)
	}

	return nil
}

func readZipEntry(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open zip entry %q: %w", file.Name, err)
	}
	defer reader.Close()

	return readLimitedBytes(reader, limit, fmt.Sprintf("zip entry %q", file.Name))
}

func readLimitedBytes(reader io.Reader, limit int64, label string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, limit)
	}

	return data, nil
}
