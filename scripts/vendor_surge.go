//go:build ignore

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "Show what would change without writing files")
	force := flag.Bool("force", false, "Overwrite all files even if content is identical")
	flag.Parse()

	srcBase := filepath.Clean("third_party/Surge/internal")
	dstBase := filepath.Clean("internal/surge")

	// Subdirectories to copy (download package is required by core, testutil for tests)
	dirsToCopy := []string{"engine", "core", "processing", "utils", "download", "testutil"}

	// Exclude TUI/GUI files from source
	excludes := map[string]bool{
		filepath.Join("utils", "notify.go"):    true,
		filepath.Join("utils", "open.go"):      true,
		filepath.Join("utils", "open_test.go"): true,
	}

	// GoAria-created stub files that replace excluded upstream files.
	// These must not be deleted as "stale" during vendor sync.
	preserveFiles := map[string]bool{
		filepath.Join("utils", "notify_stub.go"): true,
	}

	// Phase 1: Collect all source files and determine what needs to change
	type fileAction struct {
		relPath   string
		content   []byte
		exists    bool
		identical bool
	}

	var actions []fileAction
	vendoredPaths := make(map[string]bool) // tracks all valid destination paths

	for _, subDir := range dirsToCopy {
		srcPath := filepath.Join(srcBase, subDir)
		err := filepath.WalkDir(srcPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(srcBase, path)
			if err != nil {
				return err
			}

			if d.IsDir() {
				return nil
			}

			if excludes[relPath] {
				return nil
			}

			content, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("error reading %s: %w", path, err)
			}

			// Replace imports
			oldImport := []byte("github.com/SurgeDM/Surge/internal/")
			newImport := []byte("goaria-v3/internal/surge/")
			modifiedContent := bytes.ReplaceAll(content, oldImport, newImport)

			dstPath := filepath.Join(dstBase, relPath)
			vendoredPaths[relPath] = true

			existing, err := os.ReadFile(dstPath)
			if err == nil {
				identical := bytes.Equal(existing, modifiedContent)
				actions = append(actions, fileAction{
					relPath:   relPath,
					content:   modifiedContent,
					exists:    true,
					identical: identical,
				})
			} else {
				actions = append(actions, fileAction{
					relPath:   relPath,
					content:   modifiedContent,
					exists:    false,
					identical: false,
				})
			}

			return nil
		})
		if err != nil {
			fmt.Printf("Error scanning %s: %v\n", subDir, err)
			os.Exit(1)
		}
	}

	// Phase 2: Detect stale files in destination that no longer exist in source
	var staleFiles []string
	for _, subDir := range dirsToCopy {
		dstDir := filepath.Join(dstBase, subDir)
		err := filepath.WalkDir(dstDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			relPath, err := filepath.Rel(dstBase, path)
			if err != nil {
				return err
			}
			if !vendoredPaths[relPath] && !preserveFiles[relPath] {
				staleFiles = append(staleFiles, relPath)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: error scanning stale files in %s: %v\n", subDir, err)
		}
	}

	// Phase 3: Report
	var toWrite, toSkip, toDelete int
	for _, a := range actions {
		if a.exists && a.identical && !*force {
			toSkip++
		} else if a.exists {
			toWrite++
			status := "MODIFIED"
			if *force {
				status = "FORCED"
			}
			if *dryRun {
				fmt.Printf("  [DRY-RUN] %s: %s\n", status, a.relPath)
			}
		} else {
			toWrite++
			if *dryRun {
				fmt.Printf("  [DRY-RUN] NEW: %s\n", a.relPath)
			}
		}
	}
	toDelete = len(staleFiles)
	for _, sf := range staleFiles {
		if *dryRun {
			fmt.Printf("  [DRY-RUN] STALE: %s\n", sf)
		}
	}

	fmt.Printf("\nSummary: %d to write, %d unchanged, %d stale\n", toWrite, toSkip, toDelete)

	if *dryRun {
		fmt.Println("Dry run complete. No files were modified.")
		return
	}

	// Phase 4: Execute writes
	written := 0
	for _, a := range actions {
		if a.exists && a.identical && !*force {
			continue
		}
		dstPath := filepath.Join(dstBase, a.relPath)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			fmt.Printf("Error creating dir for %s: %v\n", dstPath, err)
			os.Exit(1)
		}
		if err := os.WriteFile(dstPath, a.content, 0o644); err != nil {
			fmt.Printf("Error writing %s: %v\n", dstPath, err)
			os.Exit(1)
		}
		written++
	}

	// Phase 5: Delete stale files
	deleted := 0
	for _, sf := range staleFiles {
		dstPath := filepath.Join(dstBase, sf)
		if err := os.Remove(dstPath); err != nil {
			fmt.Printf("Warning: could not remove stale file %s: %v\n", sf, err)
		} else {
			deleted++
		}
	}

	fmt.Printf("Vendored: %d files written, %d stale files removed.\n", written, deleted)

	// Run golangci-lint fmt on vendored files to match project formatting.
	// The main .golangci.yml excludes internal/surge from formatters (to avoid
	// touching upstream code during normal fmt runs), so we use a temporary
	// override config that removes the exclusion for this one-off vendor fmt.
	fmt.Println("Running golangci-lint fmt on internal/surge...")

	tmpConfig, err := os.CreateTemp(".", ".golangci-vendor-*.yml")
	if err != nil {
		fmt.Printf("Warning: could not create temp config: %v\n", err)
	} else {
		defer os.Remove(tmpConfig.Name())
		overrideConfig := `version: "2"
formatters:
  enable:
    - gofumpt
    - gci
  settings:
    gofumpt:
      module-path: goaria-v3
    gci:
      custom-order: true
      sections:
        - standard
        - prefix(goaria-v3)
        - default
`
		if _, err := tmpConfig.WriteString(overrideConfig); err != nil {
			fmt.Printf("Warning: could not write temp config: %v\n", err)
		}
		tmpConfig.Close()

		fmtCmd := exec.Command("golangci-lint", "fmt", "-c", tmpConfig.Name(), "internal/surge/")
		fmtCmd.Stdout = os.Stdout
		fmtCmd.Stderr = os.Stderr
		if err := fmtCmd.Run(); err != nil {
			fmt.Printf("Warning: golangci-lint fmt failed: %v\n", err)
		} else {
			fmt.Println("Formatting complete.")
		}
	}

	fmt.Println("Surge core packages successfully vended and import paths rewritten.")
}
