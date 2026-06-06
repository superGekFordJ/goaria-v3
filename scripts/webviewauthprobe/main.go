package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const probeSummarySchemaVersion = 1

var probeCategories = map[string]struct{}{
	"script_running":               {},
	"origin_check_passed":          {},
	"session_opened":               {},
	"session_unavailable":          {},
	"session_busy":                 {},
	"window_open_failed":           {},
	"preflight_accepted":           {},
	"preflight_origin_rejected":    {},
	"preflight_method_rejected":    {},
	"preflight_header_rejected":    {},
	"post_origin_rejected":         {},
	"post_method_rejected":         {},
	"post_content_type_rejected":   {},
	"post_session_header_rejected": {},
	"post_body_rejected":           {},
	"post_payload_rejected":        {},
	"post_accepted":                {},
	"post_expired":                 {},
	"terminal_success":             {},
	"terminal_cancel":              {},
	"terminal_error":               {},
	"session_closed":               {},
}

type probeSummary struct {
	SchemaVersion int            `json:"schema_version"`
	EventsTotal   int            `json:"events_total"`
	Categories    map[string]int `json:"categories"`
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: webviewauthprobe analyze [--input path]")
	}
	switch args[0] {
	case "analyze":
		flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var inputPath string
		flags.StringVar(&inputPath, "input", "", "category JSONL input path")
		if err := flags.Parse(args[1:]); err != nil {
			return errors.New("analyze arguments are invalid")
		}
		if flags.NArg() != 0 {
			return errors.New("analyze arguments are invalid")
		}

		input := stdin
		var file *os.File
		if strings.TrimSpace(inputPath) != "" {
			opened, err := os.Open(inputPath)
			if err != nil {
				return errors.New("read diagnostic input failed")
			}
			file = opened
			input = opened
		}
		if file != nil {
			defer file.Close()
		}

		summary, err := analyze(input)
		if err != nil {
			return err
		}
		return writeSummary(stdout, summary)
	default:
		return errors.New("unknown subcommand")
	}
}

func analyze(input io.Reader) (probeSummary, error) {
	if input == nil {
		return probeSummary{}, errors.New("diagnostic input is invalid")
	}
	summary := probeSummary{SchemaVersion: probeSummarySchemaVersion, Categories: make(map[string]int)}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		category, err := decodeEventCategory(line)
		if err != nil {
			return probeSummary{}, err
		}
		summary.EventsTotal++
		summary.Categories[category]++
	}
	if err := scanner.Err(); err != nil {
		return probeSummary{}, errors.New("read diagnostic input failed")
	}

	return summary, nil
}

func decodeEventCategory(line []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var event map[string]any
	if err := decoder.Decode(&event); err != nil {
		return "", errors.New("diagnostic event is invalid")
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errors.New("diagnostic event is invalid")
	}
	if len(event) != 1 {
		return "", errors.New("diagnostic event is invalid")
	}
	for key := range event {
		if key != "category" {
			return "", errors.New("diagnostic event is invalid")
		}
	}
	value, ok := event["category"]
	if !ok {
		return "", errors.New("diagnostic event is invalid")
	}
	category, ok := value.(string)
	if !ok || strings.TrimSpace(category) != category || category == "" {
		return "", errors.New("diagnostic category is invalid")
	}
	if _, ok := probeCategories[category]; !ok {
		return "", errors.New("diagnostic category is invalid")
	}

	return category, nil
}

func writeSummary(output io.Writer, summary probeSummary) error {
	if output == nil {
		return errors.New("summary output is invalid")
	}
	ordered := struct {
		SchemaVersion int            `json:"schema_version"`
		EventsTotal   int            `json:"events_total"`
		Categories    map[string]int `json:"categories"`
	}{
		SchemaVersion: summary.SchemaVersion,
		EventsTotal:   summary.EventsTotal,
		Categories:    orderedCategoryCounts(summary.Categories),
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(ordered); err != nil {
		return errors.New("write summary failed")
	}

	return nil
}

func orderedCategoryCounts(input map[string]int) map[string]int {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]int, len(input))
	for _, key := range keys {
		ordered[key] = input[key]
	}

	return ordered
}
