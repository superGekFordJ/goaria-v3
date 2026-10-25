package orchestrator

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"goaria-v3/internal/surge/config"
	probing "goaria-v3/internal/surge/probe"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// InferFilenameFromURL is the final naming fallback when neither the user nor
// the probe produced a trustworthy filename.
func InferFilenameFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}

	isSafeComponent := func(name string) bool {
		name = strings.TrimSpace(name)
		if name == "" {
			return false
		}
		// Keep URL-derived names inside the chosen destination directory.
		if strings.Contains(name, "/") || strings.Contains(name, "\\") {
			return false
		}
		if name == "." || name == ".." || name == "/" {
			return false
		}
		// Reject simple Windows absolute paths like C:foo or C:\foo
		if len(name) >= 2 && (name[1] == ':' && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z'))) {
			return false
		}
		return true
	}

	query := parsed.Query()
	if name := strings.TrimSpace(query.Get("filename")); name != "" {
		if base := strings.TrimSpace(path.Base(name)); isSafeComponent(base) {
			utils.Debug("Inferred filename from query param 'filename': %s", base)
			return base
		}
	}
	if name := strings.TrimSpace(query.Get("file")); name != "" {
		if base := strings.TrimSpace(path.Base(name)); isSafeComponent(base) {
			utils.Debug("Inferred filename from query param 'file': %s", base)
			return base
		}
	}

	base := strings.TrimSpace(path.Base(parsed.Path))
	if !isSafeComponent(base) {
		return ""
	}
	utils.Debug("Inferred filename from URL path: %s", base)
	return base
}

// GetUniqueFilename keeps final files and .surge working files in the same
// collision namespace so concurrent or resumed downloads do not share a path.
func GetUniqueFilename(dir, filename string, isNameActive func(string, string) bool) string {
	if filename == "" {
		return filename
	}

	// Treat every candidate as a single file name so routing cannot escape dir.
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return filename
	}
	baseName := filepath.Base(filename)
	if baseName != filename {
		filename = baseName
	}
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || filename == "." || filename == ".." {
		return ""
	}

	existsOnDisk := func(name string) bool {
		targetPath := filepath.Join(dir, name)
		if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
			return true
		}
		// A .surge sibling means another active or recoverable download already
		// claimed this filename, so we must not hand it out again.
		if _, err := os.Stat(targetPath + types.IncompleteSuffix); !os.IsNotExist(err) {
			return true
		}
		return false
	}

	existsAnywhere := func(name string) bool {
		if isNameActive != nil && isNameActive(dir, name) {
			return true
		}
		return existsOnDisk(name)
	}

	if !existsAnywhere(filename) {
		return filename
	}

	// Keep suffixing from the same base so repeated retries stay deterministic.
	ext := filepath.Ext(filename)
	name := strings.TrimSuffix(filename, ext)

	base := name
	counter := 1

	cleanName := strings.TrimSpace(name)
	if len(cleanName) > 3 && cleanName[len(cleanName)-1] == ')' {
		if openParen := strings.LastIndexByte(cleanName, '('); openParen != -1 {
			numStr := cleanName[openParen+1 : len(cleanName)-1]
			if num, err := strconv.Atoi(numStr); err == nil && num > 0 {
				base = cleanName[:openParen]
				counter = num + 1
			}
		}
	}

	for i := range 100 { // Try next 100 numbers
		candidate := fmt.Sprintf("%s(%d)%s", base, counter+i, ext)
		if !existsAnywhere(candidate) {
			return candidate
		}
	}

	return ""
}

// GetCategoryPath applies category routing only while the caller is still using
// the default destination, so explicit user paths are left untouched.
func GetCategoryPath(filename, defaultDir string, settings *config.Settings) (string, error) {
	if settings == nil || !config.Resolve[bool](settings.Categories.CategoryEnabled) || filename == "" {
		return defaultDir, nil
	}

	cat, err := config.GetCategoryForFile(filename, settings.Categories.Categories)
	if err != nil {
		return defaultDir, fmt.Errorf("category match error for %s: %w", filename, err)
	}

	if cat != nil {
		if catPath := config.ResolveCategoryPath(cat, defaultDir); catPath != "" {
			return utils.EnsureAbsPath(catPath), nil
		}
	}

	return defaultDir, nil
}

func getBaseFilename(url, candidate string, pr *probing.ProbeResult) string {
	if candidate != "" {
		return candidate
	}
	if pr != nil {
		if pr.DetectedFilename != "" {
			return pr.DetectedFilename
		}
	}
	inferred := InferFilenameFromURL(url)
	if inferred != "" {
		return inferred
	}
	return "download.bin"
}

// ResolveDestination centralizes routing and naming so CLI, TUI, and API
// requests all land on the same final path before the engine starts downloading.
func ResolveDestination(url, candidateFilename, defaultDir string, routeToCategory bool, settings *config.Settings, pr *probing.ProbeResult, isNameActive func(string, string) bool) (string, string, error) {
	filename := getBaseFilename(url, candidateFilename, pr)

	destPath := defaultDir
	if routeToCategory && settings != nil && config.Resolve[bool](settings.Categories.CategoryEnabled) && filename != "" {
		var err error
		destPath, err = GetCategoryPath(filename, defaultDir, settings)
		if err != nil {
			return "", "", err
		}
	}

	// Safety: Truncate early so GetUniqueFilename has room to append a suffix
	filename = utils.TruncateFilename(filename)

	finalFilename := GetUniqueFilename(destPath, filename, isNameActive)
	if finalFilename == "" {
		return "", "", fmt.Errorf("could not determine a unique filename for %s", url)
	}

	return destPath, finalFilename, nil
}

// RemoveIncompleteFile drops only the reserved working file, leaving any
// promoted final file untouched.
func RemoveIncompleteFile(destPath string) error {
	if destPath == "" {
		return nil
	}
	surgePath := destPath + types.IncompleteSuffix
	if err := retryRemove(surgePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveSpillFile drops the canonical HLS spill arena ".<id>.spill" sitting
// next to destPath. The id is the raw download ID — never the value a
// persisted checkpoint claims — and must form one legal filename
// component, mirroring the arena-side validateSpillID rules so an
// actionable error replaces any accidental traversal. An empty or "."
// destination and a missing file are success; every other failure
// surfaces to the caller so repeated removal stays idempotent without
// hiding real errors. RemoveAll is never used — only the enumerated file.
func RemoveSpillFile(destPath, id string) error {
	if destPath == "" || destPath == "." || id == "" {
		return nil
	}
	if err := validateSpillFileID(id); err != nil {
		return err
	}
	spillPath := filepath.Join(filepath.Dir(destPath), "."+id+".spill")
	if err := retryRemove(spillPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// validateSpillFileID mirrors the HLS arena's download-ID rules: the ID
// becomes the whole middle of ".<id>.spill", so it must be a single
// non-empty filename component — no traversal, no separators, no control
// or Windows-invalid characters, no leading/trailing dot-space form.
func validateSpillFileID(id string) error {
	if strings.Contains(id, "..") {
		return fmt.Errorf("spill id %q contains traversal", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:?"<>|*`, r) {
			return fmt.Errorf("spill id %q has illegal filename character %q", id, r)
		}
	}
	if id[len(id)-1] == '.' || id[len(id)-1] == ' ' || id[0] == ' ' {
		return fmt.Errorf("spill id %q has trailing/leading dot-space form", id)
	}
	return nil
}
