package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type benchEntry struct {
	raw  string
	nsOp float64
}

type pkgGroup struct {
	pkgLine    string
	benchOrder []string
	benchmarks map[string][]benchEntry
}

var nsOpRegex = regexp.MustCompile(`([0-9.]+)\s+ns/op`)

func run(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: go run ./scripts/benchmedian <input-file> [output-file]")
	}

	inputFile := args[1]
	f, err := os.Open(inputFile)
	if err != nil {
		return fmt.Errorf("error opening input file: %w", err)
	}
	defer f.Close()

	var pkgOrder []string
	pkgGroups := make(map[string]*pkgGroup)
	currentPkg := "default"

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "pkg:") {
			currentPkg = trimmed
			if _, exists := pkgGroups[currentPkg]; !exists {
				pkgOrder = append(pkgOrder, currentPkg)
				pkgGroups[currentPkg] = &pkgGroup{
					pkgLine:    trimmed,
					benchmarks: make(map[string][]benchEntry),
				}
			}
			continue
		}

		if !strings.HasPrefix(trimmed, "Benchmark") {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}

		name := fields[0]
		matches := nsOpRegex.FindStringSubmatch(trimmed)
		if len(matches) < 2 {
			continue
		}

		val, err := strconv.ParseFloat(matches[1], 64)
		if err != nil {
			continue
		}

		group, exists := pkgGroups[currentPkg]
		if !exists {
			group = &pkgGroup{
				pkgLine:    currentPkg,
				benchmarks: make(map[string][]benchEntry),
			}
			pkgGroups[currentPkg] = group
			pkgOrder = append(pkgOrder, currentPkg)
		}

		if _, benchExists := group.benchmarks[name]; !benchExists {
			group.benchOrder = append(group.benchOrder, name)
		}
		group.benchmarks[name] = append(group.benchmarks[name], benchEntry{
			raw:  trimmed,
			nsOp: val,
		})
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading input file: %w", err)
	}

	var outLines []string
	for _, pkgKey := range pkgOrder {
		group := pkgGroups[pkgKey]
		if group.pkgLine != "default" {
			outLines = append(outLines, group.pkgLine)
		}
		for _, name := range group.benchOrder {
			entries := group.benchmarks[name]
			if len(entries) == 0 {
				continue
			}
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].nsOp < entries[j].nsOp
			})
			medianIdx := len(entries) / 2
			outLines = append(outLines, entries[medianIdx].raw)
		}
	}

	outContent := strings.Join(outLines, "\n") + "\n"

	if len(args) >= 3 {
		outputFile := args[2]
		if err := os.WriteFile(outputFile, []byte(outContent), 0o644); err != nil {
			return fmt.Errorf("error writing output file: %w", err)
		}
	} else {
		fmt.Print(outContent)
	}

	return nil
}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
