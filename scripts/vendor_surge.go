//go:build ignore

package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	srcBase := filepath.Clean("third_party/Surge/internal")
	dstBase := filepath.Clean("internal/surge")

	// Subdirectories to copy (download package is required by core, testutil for tests)
	dirsToCopy := []string{"engine", "core", "processing", "utils", "download", "testutil"}

	// Clean target directories first to avoid stale files
	for _, dir := range dirsToCopy {
		targetDir := filepath.Join(dstBase, dir)
		if err := os.RemoveAll(targetDir); err != nil {
			fmt.Printf("Error cleaning target dir %s: %v\n", targetDir, err)
			os.Exit(1)
		}
	}

	// Exclude TUI/GUI files
	excludes := map[string]bool{
		filepath.Join("utils", "notify.go"):    true,
		filepath.Join("utils", "open.go"):      true,
		filepath.Join("utils", "open_test.go"): true,
	}

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
				fmt.Printf("Skipping excluded file: %s\n", relPath)
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

			// Write to destination
			dstPath := filepath.Join(dstBase, relPath)
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return fmt.Errorf("error creating dir for %s: %w", dstPath, err)
			}

			if err := os.WriteFile(dstPath, modifiedContent, 0o644); err != nil {
				return fmt.Errorf("error writing to %s: %w", dstPath, err)
			}

			return nil
		})
		if err != nil {
			fmt.Printf("Error during vendor of %s: %v\n", subDir, err)
			os.Exit(1)
		}
	}

	fmt.Println("Surge core packages successfully vended and import paths rewritten.")
}
