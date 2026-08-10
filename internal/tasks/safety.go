package tasks

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

func SafeAria2OutFilename(filename string) (string, error) {
	trimmed := strings.TrimSpace(filename)
	if trimmed == "" {
		return "", errors.New("filename must be non-empty after trim")
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return "", errors.New("filename must not contain CR/LF")
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		return "", errors.New("filename must not contain path separators")
	}
	if trimmed == "." || trimmed == ".." || strings.Contains(trimmed, "..") {
		return "", errors.New("filename must not contain dot-dot path traversal")
	}
	if filepath.IsAbs(trimmed) || (runtime.GOOS != "windows" && isWindowsAbsPath(trimmed)) {
		return "", errors.New("filename must not be an absolute path")
	}
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		return "", errors.New("filename must not contain a drive prefix")
	}
	if filepath.Base(trimmed) != trimmed {
		return "", errors.New("filename must be a base name")
	}

	return trimmed, nil
}

func isWindowsAbsPath(value string) bool {
	if len(value) >= 3 && isASCIILetter(value[0]) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}

	return strings.HasPrefix(value, `\\`)
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func redactAssignmentValues(input string) string {
	markers := []string{"token=", "secret=", "auth=", "key="}
	var builder strings.Builder
	lower := strings.ToLower(input)
	for offset := 0; offset < len(input); {
		match := -1
		marker := ""
		for _, candidate := range markers {
			if idx := strings.Index(lower[offset:], candidate); idx >= 0 && (match < 0 || idx < match) {
				match = idx
				marker = candidate
			}
		}
		if match < 0 {
			builder.WriteString(input[offset:])
			break
		}

		start := offset + match
		valueStart := start + len(marker)
		valueEnd := valueStart
		for valueEnd < len(input) && !strings.ContainsRune(" \t\r\n&;,'\"`,)", rune(input[valueEnd])) {
			valueEnd++
		}

		builder.WriteString(input[offset:valueStart])
		if valueEnd > valueStart {
			builder.WriteString("[REDACTED]")
		}
		offset = valueEnd
	}

	return builder.String()
}
