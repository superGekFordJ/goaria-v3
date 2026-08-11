//go:build ignore

// Vendor Surge core packages from third_party/Surge into internal/surge.
// Vendor source branch: goaria-fork-v2 (Plan C / #522 tree). goaria-fork is
// the read-only patch archive only — do not vendor from it.
//
// WRITE hold lifted after Inventory §5.4 unlock (ports + adapters ready).
// Plain WRITE is allowed; -force remains permanently rejected.
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

// vendorWriteBlocked=false: plain dry-run and WRITE allowed from goaria-fork-v2.
// -force stays rejected regardless of this flag.
const vendorWriteBlocked = false

func main() {
	dryRun := flag.Bool("dry-run", false, "Show what would change without writing files")
	force := flag.Bool("force", false, "Overwrite all files even if content is identical")
	flag.Parse()

	// -force is never allowed (would overwrite preserveFiles / GoAria-only paths).
	if *force {
		fmt.Fprintln(os.Stderr, "error: -force is permanently blocked.")
		fmt.Fprintln(os.Stderr, "Use plain WRITE (content-diff only) or -dry-run; never overwrite preserve paths.")
		os.Exit(2)
	}

	if vendorWriteBlocked {
		if !*dryRun {
			fmt.Fprintln(os.Stderr, "error: vendor WRITE is blocked by Plan C hold (Inventory §5.4).")
			fmt.Fprintln(os.Stderr, "Only -dry-run is allowed until dual-path / ports and adapter readiness; plain WRITE would mutate shared utils/testutil under the live old tree.")
			fmt.Fprintln(os.Stderr, "Do not push remotes; lift this gate locally when Inventory §5.4 is satisfied.")
			os.Exit(2)
		}
	}

	srcBase := filepath.Clean("third_party/Surge/internal")
	dstBase := filepath.Clean("internal/surge")

	// #522 package tree from goaria-fork-v2 (Inventory §4). Never vendor config/.
	dirsToCopy := []string{
		"strategy", "orchestrator", "service", "scheduler",
		"progress", "transport", "store", "types",
		"utils", "testutil", "probe",
	}

	// Exclude TUI/GUI files from source
	excludes := map[string]bool{
		filepath.Join("utils", "notify.go"):    true,
		filepath.Join("utils", "open.go"):      true,
		filepath.Join("utils", "open_test.go"): true,
		// text.go pulls charmbracelet/x/ansi (+ displaywidth, go-colorful,
		// go-runewidth) purely for upstream TUI rendering. None of its 4
		// exported helpers (WrapText/Truncate/TruncateMiddle/TruncateTwoLines)
		// are called inside the vendored slice — only by upstream internal/tui,
		// which is not vendored. Excluding it drops 4 indirect deps from go.mod.
		filepath.Join("utils", "text.go"):      true,
		filepath.Join("utils", "text_test.go"): true,
	}

	// GoAria-only paths that must survive stale prune. Keys use filepath.Join
	// so Windows Walk Rel matches.
	preserveFiles := map[string]bool{
		// Permanent: replaces excluded upstream notify.go (path unchanged post-#522).
		filepath.Join("utils", "notify_stub.go"): true,

		// GoAria-only progress regression (relocated from engine/types/).
		filepath.Join("progress", "recalculate_progress_partial_chunk_test.go"): true,

		// GoAria AppConfig TestMain overlays on tip goleak harnesses.
		filepath.Join("orchestrator", "main_test.go"): true,
		filepath.Join("service", "main_test.go"):      true,
		filepath.Join("scheduler", "main_test.go"):    true,
	}

	// Phase 1: Read source files, replace imports, and write pre-fmt content
	// to a temp directory. We format the temp dir before comparing so that
	// the comparison is post-fmt vs post-fmt (existing destination files were
	// formatted in previous vendor runs). Without this, whitespace differences
	// between pre-fmt and post-fmt would cause false-positive MODIFIED reports.
	type fileAction struct {
		relPath   string
		content   []byte // formatted content (after golangci-lint fmt)
		exists    bool
		identical bool
	}

	var actions []fileAction
	vendoredPaths := make(map[string]bool) // tracks all valid destination paths

	tmpDir, err := os.MkdirTemp("", "surge-vendor-")
	if err != nil {
		fmt.Printf("Error creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

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

			vendoredPaths[relPath] = true

			// Write pre-fmt content to temp dir for formatting
			tmpPath := filepath.Join(tmpDir, relPath)
			if err := os.MkdirAll(filepath.Dir(tmpPath), 0o755); err != nil {
				return fmt.Errorf("error creating temp dir for %s: %w", relPath, err)
			}
			if err := os.WriteFile(tmpPath, modifiedContent, 0o644); err != nil {
				return fmt.Errorf("error writing temp %s: %w", relPath, err)
			}

			actions = append(actions, fileAction{
				relPath: relPath,
				exists:  false, // will be determined after fmt + comparison
			})

			return nil
		})
		if err != nil {
			fmt.Printf("Error scanning %s: %v\n", subDir, err)
			os.Exit(1)
		}
	}

	// Phase 2: Run golangci-lint fmt on the temp directory.
	// This formats all pre-fmt content so comparisons are accurate.
	fmt.Println("Running golangci-lint fmt on temp vendor dir...")

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

		fmtCmd := exec.Command("golangci-lint", "fmt", "-c", tmpConfig.Name(), tmpDir+"/")
		fmtCmd.Stdout = os.Stdout
		fmtCmd.Stderr = os.Stderr
		if err := fmtCmd.Run(); err != nil {
			fmt.Printf("Warning: golangci-lint fmt failed: %v\n", err)
		} else {
			fmt.Println("Temp formatting complete.")
		}
	}

	// Phase 3: Read back formatted content from temp dir and compare against
	// existing destination files.
	for i := range actions {
		tmpPath := filepath.Join(tmpDir, actions[i].relPath)
		formatted, err := os.ReadFile(tmpPath)
		if err != nil {
			fmt.Printf("Error reading formatted temp %s: %v\n", actions[i].relPath, err)
			os.Exit(1)
		}
		actions[i].content = formatted

		dstPath := filepath.Join(dstBase, actions[i].relPath)
		existing, err := os.ReadFile(dstPath)
		if err == nil {
			actions[i].exists = true
			actions[i].identical = bytes.Equal(existing, formatted)
		} else {
			actions[i].exists = false
			actions[i].identical = false
		}
	}

	// Phase 4: Detect stale files in destination that no longer exist in source
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

	// Phase 5: Report
	var toWrite, toSkip, toDelete int
	for _, a := range actions {
		if preserveFiles[a.relPath] {
			toSkip++
			continue
		}
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

	// Phase 6: Execute writes (formatted content)
	written := 0
	for _, a := range actions {
		if a.exists && a.identical && !*force {
			continue
		}
		// Never overwrite GoAria-only preserve overlays (e.g. AppConfig TestMain).
		if preserveFiles[a.relPath] {
			toSkip++
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

	// Phase 7: Delete stale files
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
	fmt.Println("Surge core packages successfully vended and import paths rewritten.")

	// Phase 8: Lint vendored code with internal/surge exclusion removed.
	// This surfaces lint issues (including in fork-patch code) that the main
	// .golangci.yml excludes. Does not fail the vendor — just reports.
	lintConfig, err := os.CreateTemp(".", ".golangci-vendor-lint-*.yml")
	if err != nil {
		fmt.Printf("Warning: could not create temp lint config: %v\n", err)
		return
	}
	defer os.Remove(lintConfig.Name())
	lintOverride := `version: "2"
run:
  timeout: 5m
  modules-download-mode: readonly
  tests: true
linters:
  default: none
  enable:
    - govet
    - staticcheck
    - unused
    - ineffassign
    - errorlint
    - bodyclose
    - bidichk
    - asciicheck
    - gocheckcompilerdirectives
    - nolintlint
    - misspell
    - copyloopvar
    - durationcheck
    - nosprintfhostport
    - errchkjson
    - canonicalheader
    - rowserrcheck
    - sqlclosecheck
    - nilerr
    - nilnesserr
    - unconvert
    - gocritic
  settings:
    copyloopvar:
      check-alias: true
issues:
  max-issues-per-linter: 0
  max-same-issues: 0
`
	if _, err := lintConfig.WriteString(lintOverride); err != nil {
		fmt.Printf("Warning: could not write temp lint config: %v\n", err)
		return
	}
	lintConfig.Close()

	fmt.Println("\nRunning golangci-lint run on internal/surge (exclusion removed)...")
	lintCmd := exec.Command("golangci-lint", "run", "-c", lintConfig.Name(), "./internal/surge/...")
	lintCmd.Stdout = os.Stdout
	lintCmd.Stderr = os.Stderr
	if err := lintCmd.Run(); err != nil {
		fmt.Printf("⚠ Lint issues found in vendored code (does not block vendor).\n")
		fmt.Printf("  Fork-patch code (// FORK-PATCH:) should be fixed in the goaria-fork-v2 branch.\n")
		fmt.Printf("  Upstream issues should be reported or tolerated.\n")
	} else {
		fmt.Println("No lint issues found in vendored code.")
	}
}
