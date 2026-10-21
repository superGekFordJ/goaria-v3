package hls

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"goaria-v3/internal/surge/utils"
)

// genericHLSStemRegex matches the placeholder basenames common in HLS manifest
// URLs (index.m3u8, playlist-1080p.m3u8, chunklist_b2000000.m3u8, ...).
var genericHLSStemRegex = regexp.MustCompile(`^(?i)(index|playlist|master|chunklist|manifest|stream|video|hls|media)([-_].*)?$`)

// IsGenericHLSStem reports whether stem is empty or a generic placeholder that
// carries no useful naming information.
func IsGenericHLSStem(stem string) bool {
	stem = strings.TrimSpace(stem)
	return stem == "" || genericHLSStemRegex.MatchString(stem)
}

// ContainerFormatExtension maps a manifest container format onto the output
// file extension: fragmented MP4 produces .mp4, everything else .ts.
func ContainerFormatExtension(containerFormat string) string {
	switch strings.ToLower(strings.TrimSpace(containerFormat)) {
	case ContainerFMP4, "mp4":
		return ".mp4"
	default:
		return ".ts"
	}
}

var windowsReservedStems = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// isWindowsReservedStem reports whether s is a Windows reserved device name,
// ignoring case and any extension following the first dot.
func isWindowsReservedStem(s string) bool {
	base := s
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return windowsReservedStems[strings.ToUpper(strings.TrimSpace(base))]
}

// sanitizeHLSCandidate replaces path-illegal characters (ASCII plus the
// full-width variants of : " * / < > \ | ?), control characters, and ".."
// with '_'.
func sanitizeHLSCandidate(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		switch {
		case c < 32 || (c >= 0x7F && c <= 0x9F):
			b.WriteRune('_')
		case isIllegalFilenameRune(c):
			b.WriteRune('_')
		default:
			b.WriteRune(c)
		}
	}
	return strings.ReplaceAll(b.String(), "..", "_")
}

// isIllegalFilenameRune reports whether c is path-illegal on Windows/POSIX,
// including the full-width variants commonly seen in page titles.
func isIllegalFilenameRune(c rune) bool {
	switch c {
	case '/', '\\', '<', '>', ':', '"', '|', '?', '*':
		return true
	}
	// Full-width variants (U+FF01-FF5E) map to ASCII at c-0xFEE0.
	if c >= 0xFF01 && c <= 0xFF5E {
		return isIllegalFilenameRune(c - 0xFEE0)
	}
	return false
}

// cleanCandidateStem reduces a user-supplied name, page title, URL path
// segment, or host name to a sanitized filename stem. It strips a trailing
// .m3u8/.m3u (hadManifestExt) or .mp4/.ts, and returns "" for names that are
// empty, pathological, or a Windows reserved device name.
func cleanCandidateStem(raw string) (stem string, hadManifestExt bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	s = strings.ReplaceAll(s, "\\", "/")
	seg := ""
	parts := strings.Split(s, "/")
	for _, p := range slices.Backward(parts) {
		if p = strings.TrimSpace(p); p != "" {
			seg = p
			break
		}
	}
	if seg == "" {
		return "", false
	}
	seg = sanitizeHLSCandidate(seg)
	switch lower := strings.ToLower(seg); {
	case strings.HasSuffix(lower, ".m3u8"):
		seg, hadManifestExt = seg[:len(seg)-len(".m3u8")], true
	case strings.HasSuffix(lower, ".m3u"):
		seg, hadManifestExt = seg[:len(seg)-len(".m3u")], true
	case strings.HasSuffix(lower, ".mp4"):
		seg = seg[:len(seg)-len(".mp4")]
	case strings.HasSuffix(lower, ".ts"):
		seg = seg[:len(seg)-len(".ts")]
	}
	seg = strings.TrimRight(strings.TrimSpace(seg), ". ")
	if seg == "" || seg == "." || seg == ".." || seg == "_" {
		return "", hadManifestExt
	}
	if isWindowsReservedStem(seg) {
		return "", hadManifestExt
	}
	return seg, hadManifestExt
}

// InferHLSFilenameStem picks the best filename stem in priority order:
// explicit user out / page title -> last non-generic URL path basename ->
// parent path segments -> "<host>_<unix_sec>".
func InferHLSFilenameStem(rawURL, userOut, pageTitle string, now time.Time) string {
	// Priority 1a: an explicit user-provided name wins unless it merely
	// repeats a generic manifest placeholder (e.g. "index.m3u8").
	if stem, hadManifestExt := cleanCandidateStem(userOut); stem != "" &&
		(!hadManifestExt || !IsGenericHLSStem(stem)) {
		return stem
	}
	// Priority 1b: the extension-supplied page title.
	if stem, _ := cleanCandidateStem(pageTitle); stem != "" && !IsGenericHLSStem(stem) {
		return stem
	}

	var parsed *url.URL
	if p, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		parsed = p
	}
	// Priorities 2 & 3: walk URL path segments right-to-left for the first
	// non-generic name.
	if parsed != nil && parsed.Path != "" {
		var segs []string
		for seg := range strings.SplitSeq(parsed.Path, "/") {
			if seg != "" {
				segs = append(segs, seg)
			}
		}
		for _, cand := range slices.Backward(segs) {
			if u, err := url.PathUnescape(cand); err == nil {
				cand = u
			}
			if stem, _ := cleanCandidateStem(cand); stem != "" && !IsGenericHLSStem(stem) {
				return stem
			}
		}
	}

	// Priority 4: <host>_<unix_sec> fallback.
	if now.IsZero() {
		now = time.Now()
	}
	host := "hls"
	if parsed != nil {
		if h, _ := cleanCandidateStem(parsed.Hostname()); h != "" {
			host = h
		}
	}
	return fmt.Sprintf("%s_%d", host, now.Unix())
}

// ResolveHLSOutputFilename returns the final sanitized output filename with a
// container-correct extension, truncated to the engine filename limit.
func ResolveHLSOutputFilename(rawURL, userOut, pageTitle, containerFormat string, now time.Time) string {
	stem := InferHLSFilenameStem(rawURL, userOut, pageTitle, now)
	return utils.TruncateFilename(stem + ContainerFormatExtension(containerFormat))
}
