package hls

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"

	"goaria-v3/internal/surge/types"
)

// PlaylistKind classifies a parsed HLS playlist document.
type PlaylistKind int

const (
	PlaylistKindUnknown PlaylistKind = iota
	PlaylistKindMaster
	PlaylistKindMedia
)

const (
	ContainerTS   = "ts"
	ContainerFMP4 = "fmp4"
)

const (
	// maxManifestKeys bounds the distinct AES-128 descriptors kept per
	// playlist so an untrusted manifest cannot force unbounded key
	// bookkeeping through thousands of unique EXT-X-KEY declarations.
	maxManifestKeys = 4096
	// maxManifestSegments bounds per-segment state allocated from an 8 MiB
	// untrusted manifest (worst case ~30 MiB of expanded slice + resolved
	// URI payloads) while still covering weeks of typical VOD playlists.
	maxManifestSegments = 1 << 18
)

// AudioGroupInfo aggregates the tracks declared for one
// #EXT-X-MEDIA:TYPE=AUDIO GROUP-ID.
type AudioGroupInfo struct {
	GroupID          string
	HasEmbeddedTrack bool // at least one audio track without a URI (muxed in)
	HasExternalTrack bool // at least one audio track with a non-empty URI
}

// VariantStream is one #EXT-X-STREAM-INF entry with a resolved media URI.
type VariantStream struct {
	URI              string // resolved against the post-redirect master URL
	RawURI           string
	Bandwidth        int64
	AverageBandwidth int64
	Width            int
	Height           int
	Codecs           string
	AudioGroupID     string
	DemuxedAudio     bool // AUDIO group missing, or has external tracks only
	Order            int  // 0-based declaration order among kept variants
}

// MasterPlaylist is the parsed master playlist document.
type MasterPlaylist struct {
	BaseURL     string
	Variants    []VariantStream
	AudioGroups map[string]*AudioGroupInfo
}

// MediaInitMap is the resolved #EXT-X-MAP media initialization section.
type MediaInitMap struct {
	URI        string // resolved against the post-redirect media URL
	ByteOffset int64
	ByteLength int64
}

// MediaPlaylist is the parsed media playlist document.
type MediaPlaylist struct {
	BaseURL             string
	TargetDuration      int64
	MediaSequenceBase   int64
	DiscontinuitySeq    int64
	PlaylistType        string
	EndList             bool
	ContainerFormat     string // ContainerTS | ContainerFMP4
	InitMap             *MediaInitMap
	TotalDurationMicros int64
	TotalDurationSec    float64
	ManifestFingerprint string
	SegmentRefURL       string
	Keys                []types.HLSKeyInfo
	Segments            []types.HLSSegmentState
}

// audioOnlyCodecPrefixes are RFC 6381 codec prefixes that only carry audio.
var audioOnlyCodecPrefixes = map[string]bool{
	"mp4a": true, "ac-3": true, "ec-3": true, "opus": true, "flac": true,
	"vorbis": true, "aac": true, "mp3": true, "alac": true, "speex": true,
}

// hlsKeyIdentity deduplicates AES-128 EXT-X-KEY declarations: every stored
// descriptor is AES-128, so (resolved URI, normalized IV) is a complete
// identity for a map key.
type hlsKeyIdentity struct {
	uri   string
	ivHex string
}

// ParsePlaylist parses an RFC 8216 playlist document into a master or media
// playlist. baseURL must be the post-redirect final response URL; every
// relative URI in the document is resolved against it and must land on
// http/https with a non-empty host.
func ParsePlaylist(content string, baseURL *url.URL) (PlaylistKind, *MasterPlaylist, *MediaPlaylist, error) {
	lines, err := splitPlaylistLines(content)
	if err != nil {
		return PlaylistKindUnknown, nil, nil, err
	}

	hasMasterTag, hasMediaTag := false, false
	for _, ln := range lines {
		if isMasterOnlyTag(ln) {
			hasMasterTag = true
		}
		if isMediaOnlyTag(ln) {
			hasMediaTag = true
		}
	}
	if hasMasterTag && hasMediaTag {
		return PlaylistKindUnknown, nil, nil,
			fmt.Errorf("%w: mixed master and media tags", types.ErrHLSInvalidManifest)
	}

	switch {
	case hasMasterTag:
		master, err := parseMasterPlaylist(lines, baseURL)
		if err != nil {
			return PlaylistKindUnknown, nil, nil, err
		}
		return PlaylistKindMaster, master, nil, nil
	case hasMediaTag:
		media, err := parseMediaPlaylist(lines, baseURL)
		if err != nil {
			return PlaylistKindUnknown, nil, nil, err
		}
		return PlaylistKindMedia, nil, media, nil
	default:
		return PlaylistKindUnknown, nil, nil,
			fmt.Errorf("%w: no master or media tags", types.ErrHLSInvalidManifest)
	}
}

// splitPlaylistLines strips an optional UTF-8 BOM, requires the first
// non-empty line to be #EXTM3U, and returns the remaining lines trimmed.
func splitPlaylistLines(content string) ([]string, error) {
	content = strings.TrimPrefix(content, "\uFEFF")
	raw := strings.Split(content, "\n")
	for i, r := range raw {
		if strings.TrimSpace(r) == "" {
			continue
		}
		if strings.TrimSpace(r) != "#EXTM3U" {
			return nil, fmt.Errorf("%w: missing #EXTM3U header", types.ErrHLSInvalidManifest)
		}
		lines := make([]string, 0, len(raw)-i-1)
		for _, rest := range raw[i+1:] {
			lines = append(lines, strings.TrimSpace(rest))
		}
		return lines, nil
	}
	return nil, fmt.Errorf("%w: empty playlist", types.ErrHLSInvalidManifest)
}

// isMasterOnlyTag reports whether ln is a tag that may only appear in a
// master playlist per RFC 8216.
func isMasterOnlyTag(ln string) bool {
	return strings.HasPrefix(ln, "#EXT-X-STREAM-INF:") ||
		strings.HasPrefix(ln, "#EXT-X-I-FRAME-STREAM-INF:") ||
		strings.HasPrefix(ln, "#EXT-X-MEDIA:") ||
		strings.HasPrefix(ln, "#EXT-X-SESSION-KEY:")
}

// isMediaOnlyTag reports whether ln is a tag that may only appear in a media
// playlist per RFC 8216.
func isMediaOnlyTag(ln string) bool {
	return strings.HasPrefix(ln, "#EXTINF:") ||
		strings.HasPrefix(ln, "#EXT-X-TARGETDURATION:") ||
		strings.HasPrefix(ln, "#EXT-X-MEDIA-SEQUENCE:") ||
		strings.HasPrefix(ln, "#EXT-X-MAP:") ||
		strings.HasPrefix(ln, "#EXT-X-KEY:") ||
		strings.HasPrefix(ln, "#EXT-X-BYTERANGE:") ||
		ln == "#EXT-X-ENDLIST" ||
		ln == "#EXT-X-I-FRAMES-ONLY"
}

// parseAttributeList parses an RFC 8216 attribute list "NAME=VALUE,...".
// Values may be double-quoted strings (commas allowed inside) or unquoted
// tokens. A missing '=' or an unclosed quote is a hard parse error.
func parseAttributeList(raw string) (map[string]string, error) {
	attrs := make(map[string]string)
	i, n := 0, len(raw)
	for i < n {
		for i < n && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		keyStart := i
		for i < n && raw[i] != '=' && raw[i] != ',' {
			i++
		}
		if i >= n || raw[i] != '=' {
			return nil, fmt.Errorf("%w: malformed attribute list %q", types.ErrHLSInvalidManifest, raw)
		}
		key := strings.TrimSpace(raw[keyStart:i])
		if key == "" {
			return nil, fmt.Errorf("%w: empty attribute name %q", types.ErrHLSInvalidManifest, raw)
		}
		i++
		if i < n && raw[i] == '"' {
			i++
			valStart := i
			for i < n && raw[i] != '"' {
				i++
			}
			if i >= n {
				return nil, fmt.Errorf("%w: unclosed quoted value for %s", types.ErrHLSInvalidManifest, key)
			}
			attrs[key] = raw[valStart:i]
			i++
			for i < n && (raw[i] == ' ' || raw[i] == '\t') {
				i++
			}
			if i < n && raw[i] != ',' {
				return nil, fmt.Errorf("%w: trailing garbage after quoted value for %s", types.ErrHLSInvalidManifest, key)
			}
		} else {
			valStart := i
			for i < n && raw[i] != ',' {
				i++
			}
			attrs[key] = strings.TrimSpace(raw[valStart:i])
		}
		if i < n {
			i++ // consume ','
		}
	}
	return attrs, nil
}

// resolveManifestURI resolves rawRef against baseURL and requires the result
// to be an absolute http/https URL with a non-empty host.
func resolveManifestURI(baseURL *url.URL, rawRef string) (string, error) {
	rawRef = strings.TrimSpace(rawRef)
	if rawRef == "" {
		return "", fmt.Errorf("%w: empty URI", types.ErrHLSInvalidManifest)
	}
	if strings.IndexFunc(rawRef, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%w: whitespace inside URI %q", types.ErrHLSInvalidManifest, rawRef)
	}
	ref, err := url.Parse(rawRef)
	if err != nil {
		return "", fmt.Errorf("%w: malformed URI %q", types.ErrHLSInvalidManifest, rawRef)
	}
	resolved := ref
	if baseURL != nil {
		resolved = baseURL.ResolveReference(ref)
	}
	if (!strings.EqualFold(resolved.Scheme, "http") && !strings.EqualFold(resolved.Scheme, "https")) ||
		resolved.Host == "" {
		return "", fmt.Errorf("%w: unsupported or relative URI %q", types.ErrHLSInvalidManifest, rawRef)
	}
	return resolved.String(), nil
}

// parseByteRangeValue parses "<n>[@<o>]" requiring n > 0 and o >= 0.
func parseByteRangeValue(v string) (length, offset int64, hasOffset bool, err error) {
	parts := strings.SplitN(strings.TrimSpace(v), "@", 2)
	length, err = strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || length <= 0 {
		return 0, 0, false, fmt.Errorf("%w: invalid byte range %q", types.ErrHLSInvalidManifest, v)
	}
	if len(parts) == 1 {
		return length, 0, false, nil
	}
	offset, err = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || offset < 0 {
		return 0, 0, false, fmt.Errorf("%w: invalid byte range %q", types.ErrHLSInvalidManifest, v)
	}
	return length, offset, true, nil
}

// normalizeIVHex validates an EXT-X-KEY IV attribute ("0x" + 1-32 hex chars)
// and returns the normalized 32-char lowercase hex form (no 0x prefix).
func normalizeIVHex(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		hexPart := s[2:]
		if len(hexPart) >= 1 && len(hexPart) <= 32 {
			valid := true
			for i := 0; i < len(hexPart); i++ {
				c := hexPart[i]
				isHex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
				if !isHex {
					valid = false
					break
				}
			}
			if valid {
				return strings.Repeat("0", 32-len(hexPart)) + strings.ToLower(hexPart), nil
			}
		}
	}
	return "", fmt.Errorf("%w: malformed IV %q", types.ErrHLSInvalidManifest, raw)
}

// parseResolution parses "<w>x<h>" (case-insensitive separator). A missing or
// malformed value yields 0x0, meaning "no usable resolution declared".
func parseResolution(s string) (width, height int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0
	}
	idx := strings.IndexAny(s, "xX")
	if idx <= 0 || idx >= len(s)-1 {
		return 0, 0
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(s[:idx]))
	h, err2 := strconv.Atoi(strings.TrimSpace(s[idx+1:]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0
	}
	return w, h
}

// isAudioOnlyVariant reports whether a variant declares no usable resolution
// and a non-empty CODECS list made up exclusively of known audio-only codec
// prefixes. Unknown or mixed codecs keep the variant eligible.
func isAudioOnlyVariant(width, height int, codecs string) bool {
	if width != 0 || height != 0 {
		return false
	}
	codecs = strings.TrimSpace(codecs)
	if codecs == "" {
		return false
	}
	for c := range strings.SplitSeq(codecs, ",") {
		prefix := strings.ToLower(strings.TrimSpace(c))
		if i := strings.IndexByte(prefix, '.'); i >= 0 {
			prefix = prefix[:i]
		}
		if prefix == "" {
			continue
		}
		if !audioOnlyCodecPrefixes[prefix] {
			return false
		}
	}
	return true
}

// parseMasterPlaylist parses master-only tags into a MasterPlaylist.
func parseMasterPlaylist(lines []string, baseURL *url.URL) (*MasterPlaylist, error) {
	master := &MasterPlaylist{AudioGroups: make(map[string]*AudioGroupInfo)}
	if baseURL != nil {
		master.BaseURL = baseURL.String()
	}

	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if ln == "" || ln == "#EXTM3U" {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "#EXT-X-STREAM-INF:"):
			attrs, err := parseAttributeList(ln[len("#EXT-X-STREAM-INF:"):])
			if err != nil {
				return nil, err
			}
			bandwidth, err := strconv.ParseInt(strings.TrimSpace(attrs["BANDWIDTH"]), 10, 64)
			if err != nil || bandwidth <= 0 {
				return nil, fmt.Errorf("%w: bad BANDWIDTH %q", types.ErrHLSInvalidManifest, attrs["BANDWIDTH"])
			}
			avgBandwidth, _ := strconv.ParseInt(strings.TrimSpace(attrs["AVERAGE-BANDWIDTH"]), 10, 64)
			if avgBandwidth < 0 {
				avgBandwidth = 0
			}
			width, height := parseResolution(attrs["RESOLUTION"])

			// The URI line must be the next non-empty, non-comment line;
			// an intervening tag violates RFC 8216 and is rejected instead
			// of silently dropping gating info such as EXT-X-MEDIA.
			uriLine := ""
			for j := i + 1; j < len(lines); j++ {
				s := lines[j]
				if s == "" {
					continue
				}
				if strings.HasPrefix(s, "#") {
					if strings.HasPrefix(s, "#EXT") {
						return nil, fmt.Errorf("%w: tag line %q between STREAM-INF and URI", types.ErrHLSInvalidManifest, s)
					}
					continue
				}
				uriLine, i = s, j
				break
			}
			if uriLine == "" {
				return nil, fmt.Errorf("%w: STREAM-INF without URI line", types.ErrHLSInvalidManifest)
			}
			if isAudioOnlyVariant(width, height, attrs["CODECS"]) {
				continue
			}
			resolved, err := resolveManifestURI(baseURL, uriLine)
			if err != nil {
				return nil, err
			}
			master.Variants = append(master.Variants, VariantStream{
				URI:              resolved,
				RawURI:           uriLine,
				Bandwidth:        bandwidth,
				AverageBandwidth: avgBandwidth,
				Width:            width,
				Height:           height,
				Codecs:           attrs["CODECS"],
				AudioGroupID:     attrs["AUDIO"],
				Order:            len(master.Variants),
			})

		case strings.HasPrefix(ln, "#EXT-X-I-FRAME-STREAM-INF:"):
			// Single-line tag: i-frame playlists are never selected.

		case strings.HasPrefix(ln, "#EXT-X-MEDIA:"):
			attrs, err := parseAttributeList(ln[len("#EXT-X-MEDIA:"):])
			if err != nil {
				return nil, err
			}
			if !strings.EqualFold(attrs["TYPE"], "AUDIO") {
				continue
			}
			gid := attrs["GROUP-ID"]
			if gid == "" {
				return nil, fmt.Errorf("%w: EXT-X-MEDIA TYPE=AUDIO without GROUP-ID", types.ErrHLSInvalidManifest)
			}
			g := master.AudioGroups[gid]
			if g == nil {
				g = &AudioGroupInfo{GroupID: gid}
				master.AudioGroups[gid] = g
			}
			if strings.TrimSpace(attrs["URI"]) != "" {
				g.HasExternalTrack = true
			} else {
				g.HasEmbeddedTrack = true
			}

		case strings.HasPrefix(ln, "#EXT-X-SESSION-KEY:"):
			attrs, err := parseAttributeList(ln[len("#EXT-X-SESSION-KEY:"):])
			if err != nil {
				return nil, err
			}
			if m := attrs["METHOD"]; m != "NONE" && m != "AES-128" {
				return nil, fmt.Errorf("%w: session key method %q", types.ErrHLSDRMNotSupported, m)
			}
			if kf := attrs["KEYFORMAT"]; kf != "" && !strings.EqualFold(kf, "identity") {
				return nil, fmt.Errorf("%w: session keyformat %q", types.ErrHLSDRMNotSupported, kf)
			}

		default:
			if !strings.HasPrefix(ln, "#") {
				return nil, fmt.Errorf("%w: stray URI line in master playlist %q", types.ErrHLSInvalidManifest, ln)
			}
			// Unrecognized tags and comments are ignored.
		}
	}

	for i := range master.Variants {
		v := &master.Variants[i]
		if v.AudioGroupID == "" {
			continue
		}
		g := master.AudioGroups[v.AudioGroupID]
		// A dangling AUDIO reference cannot supply a track either — keep it
		// out of the muxed pool so it can never yield silent video.
		v.DemuxedAudio = g == nil || (g.HasExternalTrack && !g.HasEmbeddedTrack)
	}
	if len(master.Variants) == 0 {
		return nil, fmt.Errorf("%w: master playlist has no video variant", types.ErrHLSInvalidManifest)
	}
	return master, nil
}

// parseMediaPlaylist parses media-only tags into a MediaPlaylist, applying
// the VOD/DRM/discontinuity gates defined for the downloader.
func parseMediaPlaylist(lines []string, baseURL *url.URL) (*MediaPlaylist, error) {
	media := &MediaPlaylist{ContainerFormat: ContainerTS}
	if baseURL != nil {
		media.BaseURL = baseURL.String()
	}

	var (
		segments                 []types.HLSSegmentState
		keys                     []types.HLSKeyInfo
		keyIndexByID             = make(map[hlsKeyIdentity]int)
		initMap                  *MediaInitMap
		discontinuityAfterMap    bool
		seenDiscontinuity        bool
		endList                  bool
		mediaSeqSeen             bool
		discontSeqSeen           bool
		currentKeyIndex          int
		currentIVHex             string
		hasPendingInf            bool
		pendingDurMicros         int64
		hasPendingByteRange      bool
		pendingByteLen           int64
		pendingByteOff           int64
		pendingByteOffIsExplicit bool
		totalDurMicros           int64
	)

	for _, ln := range lines {
		if ln == "" || ln == "#EXTM3U" {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "#EXT-X-MEDIA-SEQUENCE:"):
			// RFC 8216: a single declaration before the first Media Segment;
			// a late or repeated tag would silently rebase Sequence numbers.
			if mediaSeqSeen || len(segments) > 0 {
				return nil, fmt.Errorf("%w: misplaced or repeated EXT-X-MEDIA-SEQUENCE", types.ErrHLSInvalidManifest)
			}
			mediaSeqSeen = true
			v, err := strconv.ParseInt(strings.TrimSpace(ln[len("#EXT-X-MEDIA-SEQUENCE:"):]), 10, 64)
			if err != nil || v < 0 {
				return nil, fmt.Errorf("%w: bad media sequence %q", types.ErrHLSInvalidManifest, ln)
			}
			media.MediaSequenceBase = v

		case strings.HasPrefix(ln, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			// Same ordering class: redefining the discontinuity base after
			// content started makes the recorded value meaningless.
			if discontSeqSeen || seenDiscontinuity || len(segments) > 0 {
				return nil, fmt.Errorf("%w: misplaced or repeated EXT-X-DISCONTINUITY-SEQUENCE", types.ErrHLSInvalidManifest)
			}
			discontSeqSeen = true
			v, err := strconv.ParseInt(strings.TrimSpace(ln[len("#EXT-X-DISCONTINUITY-SEQUENCE:"):]), 10, 64)
			if err != nil || v < 0 {
				return nil, fmt.Errorf("%w: bad discontinuity sequence %q", types.ErrHLSInvalidManifest, ln)
			}
			media.DiscontinuitySeq = v

		case strings.HasPrefix(ln, "#EXT-X-TARGETDURATION:"):
			v, err := strconv.ParseInt(strings.TrimSpace(ln[len("#EXT-X-TARGETDURATION:"):]), 10, 64)
			if err != nil || v < 0 {
				return nil, fmt.Errorf("%w: bad target duration %q", types.ErrHLSInvalidManifest, ln)
			}
			media.TargetDuration = v

		case strings.HasPrefix(ln, "#EXT-X-PLAYLIST-TYPE:"):
			media.PlaylistType = strings.TrimSpace(ln[len("#EXT-X-PLAYLIST-TYPE:"):])

		case ln == "#EXT-X-ENDLIST":
			endList = true

		case ln == "#EXT-X-DISCONTINUITY":
			seenDiscontinuity = true
			if initMap != nil {
				discontinuityAfterMap = true
			}

		case ln == "#EXT-X-I-FRAMES-ONLY":
			// Trick-play renditions carry I-frames only; downloading one as
			// a normal stream produces unusable output.
			return nil, types.ErrHLSIFramesOnlyNotSupported

		case strings.HasPrefix(ln, "#EXT-X-MAP:"):
			attrs, err := parseAttributeList(ln[len("#EXT-X-MAP:"):])
			if err != nil {
				return nil, err
			}
			mapURI := attrs["URI"]
			if mapURI == "" {
				return nil, fmt.Errorf("%w: EXT-X-MAP without URI", types.ErrHLSInvalidManifest)
			}
			resolved, err := resolveManifestURI(baseURL, mapURI)
			if err != nil {
				return nil, err
			}
			var off, length int64
			if br := attrs["BYTERANGE"]; br != "" {
				bl, bo, _, berr := parseByteRangeValue(br)
				if berr != nil {
					return nil, berr
				}
				length, off = bl, bo
			}
			switch {
			case initMap == nil:
				initMap = &MediaInitMap{URI: resolved, ByteOffset: off, ByteLength: length}
				media.ContainerFormat = ContainerFMP4
			case discontinuityAfterMap ||
				initMap.URI != resolved ||
				initMap.ByteOffset != off ||
				initMap.ByteLength != length:
				return nil, fmt.Errorf("%w: second EXT-X-MAP (%q,%d,%d) after discontinuity or mismatch",
					types.ErrHLSDiscontinuityMapNotSupported, resolved, off, length)
			}
			// An identical re-declaration without discontinuity is idempotent.

		case strings.HasPrefix(ln, "#EXT-X-KEY:"):
			attrs, err := parseAttributeList(ln[len("#EXT-X-KEY:"):])
			if err != nil {
				return nil, err
			}
			method := attrs["METHOD"]
			if method == "" {
				return nil, fmt.Errorf("%w: EXT-X-KEY without METHOD", types.ErrHLSInvalidManifest)
			}
			switch method {
			case "NONE":
				// Reset the active encryption state; a "NONE" entry is never
				// stored in Keys so KeyIndex stays 1-based.
				currentKeyIndex, currentIVHex = 0, ""
			case "AES-128":
				if kf := attrs["KEYFORMAT"]; kf != "" && !strings.EqualFold(kf, "identity") {
					return nil, fmt.Errorf("%w: keyformat %q", types.ErrHLSDRMNotSupported, kf)
				}
				keyURI := attrs["URI"]
				if keyURI == "" {
					return nil, fmt.Errorf("%w: AES-128 key without URI", types.ErrHLSInvalidManifest)
				}
				resolved, err := resolveManifestURI(baseURL, keyURI)
				if err != nil {
					return nil, err
				}
				ivHex := ""
				if ivRaw := attrs["IV"]; ivRaw != "" {
					ivHex, err = normalizeIVHex(ivRaw)
					if err != nil {
						return nil, err
					}
				}
				// Dedup on (ResolvedURI, NormalizedIVHex) via O(1) map
				// lookup — every stored descriptor is AES-128 so the pair
				// is a complete identity. A full slice rescan per KEY line
				// would make hostile manifests quadratic.
				id := hlsKeyIdentity{uri: resolved, ivHex: ivHex}
				idx, found := keyIndexByID[id]
				if !found {
					if len(keys) >= maxManifestKeys {
						return nil, fmt.Errorf("%w: more than %d distinct EXT-X-KEY descriptors",
							types.ErrHLSInvalidManifest, maxManifestKeys)
					}
					keys = append(keys, types.HLSKeyInfo{Method: "AES-128", URI: resolved, IVHex: ivHex})
					idx = len(keys)
					keyIndexByID[id] = idx
				}
				currentKeyIndex, currentIVHex = idx, ivHex
			default:
				return nil, fmt.Errorf("%w: key method %q", types.ErrHLSDRMNotSupported, method)
			}

		case strings.HasPrefix(ln, "#EXT-X-BYTERANGE:"):
			// One pending byte-range per segment; a second tag before any
			// URI line is malformed, same as a dangling one at EOF.
			if hasPendingByteRange {
				return nil, fmt.Errorf("%w: consecutive EXT-X-BYTERANGE without a media segment", types.ErrHLSInvalidManifest)
			}
			bl, bo, hasOff, err := parseByteRangeValue(ln[len("#EXT-X-BYTERANGE:"):])
			if err != nil {
				return nil, err
			}
			pendingByteLen, pendingByteOff, pendingByteOffIsExplicit, hasPendingByteRange = bl, bo, hasOff, true

		case strings.HasPrefix(ln, "#EXTINF:"):
			if hasPendingInf {
				return nil, fmt.Errorf("%w: consecutive EXTINF without a media segment", types.ErrHLSInvalidManifest)
			}
			durStr, _, _ := strings.Cut(ln[len("#EXTINF:"):], ",")
			durSec, err := strconv.ParseFloat(strings.TrimSpace(durStr), 64)
			if err != nil || math.IsNaN(durSec) || math.IsInf(durSec, 0) || durSec < 0 || durSec > 9e12 {
				return nil, fmt.Errorf("%w: bad EXTINF duration %q", types.ErrHLSInvalidManifest, ln)
			}
			pendingDurMicros, hasPendingInf = int64(math.Round(durSec*1e6)), true

		default:
			if strings.HasPrefix(ln, "#") {
				continue // unknown tag or comment
			}
			if endList {
				return nil, fmt.Errorf("%w: media segment URI after EXT-X-ENDLIST %q", types.ErrHLSInvalidManifest, ln)
			}
			if !hasPendingInf {
				return nil, fmt.Errorf("%w: media segment URI without preceding EXTINF %q", types.ErrHLSInvalidManifest, ln)
			}
			segURI, err := resolveManifestURI(baseURL, ln)
			if err != nil {
				return nil, err
			}
			var byteOff, byteLen int64
			if hasPendingByteRange {
				byteLen = pendingByteLen
				switch {
				case pendingByteOffIsExplicit:
					byteOff = pendingByteOff
				default:
					// RFC 8216: an omitted offset continues the
					// previous byte-range segment of the same resource.
					var prev *types.HLSSegmentState
					if len(segments) > 0 {
						prev = &segments[len(segments)-1]
					}
					if prev == nil || prev.ByteLength <= 0 || prev.URI != segURI {
						return nil, fmt.Errorf("%w: EXT-X-BYTERANGE without offset cannot chain %q", types.ErrHLSInvalidManifest, ln)
					}
					if prev.ByteOffset > math.MaxInt64-prev.ByteLength {
						return nil, fmt.Errorf("%w: EXT-X-BYTERANGE offset overflow", types.ErrHLSInvalidManifest)
					}
					byteOff = prev.ByteOffset + prev.ByteLength
				}
			}
			if len(segments) >= maxManifestSegments {
				return nil, fmt.Errorf("%w: more than %d media segments", types.ErrHLSInvalidManifest, maxManifestSegments)
			}
			idx := len(segments)
			if media.MediaSequenceBase > math.MaxInt64-int64(idx) {
				return nil, fmt.Errorf("%w: media sequence base overflow", types.ErrHLSInvalidManifest)
			}
			segments = append(segments, types.HLSSegmentState{
				Index:          idx,
				Sequence:       media.MediaSequenceBase + int64(idx),
				URI:            segURI,
				DurationMicros: pendingDurMicros,
				ByteOffset:     byteOff,
				ByteLength:     byteLen,
				KeyIndex:       currentKeyIndex,
				IVHex:          currentIVHex,
			})
			if pendingDurMicros > math.MaxInt64-totalDurMicros {
				totalDurMicros = math.MaxInt64
			} else {
				totalDurMicros += pendingDurMicros
			}
			hasPendingInf, hasPendingByteRange = false, false
		}
	}

	switch {
	case hasPendingInf || hasPendingByteRange:
		return nil, fmt.Errorf("%w: dangling EXTINF/EXT-X-BYTERANGE at end of playlist", types.ErrHLSInvalidManifest)
	case !endList:
		return nil, fmt.Errorf("%w: playlist has no EXT-X-ENDLIST", types.ErrHLSLiveNotSupported)
	case len(segments) == 0:
		return nil, fmt.Errorf("%w: media playlist has no segments", types.ErrHLSInvalidManifest)
	}

	media.EndList = true
	media.InitMap = initMap
	media.TotalDurationMicros = totalDurMicros
	media.TotalDurationSec = float64(totalDurMicros) / 1e6
	media.SegmentRefURL = segments[0].URI
	media.Keys = keys
	media.Segments = segments
	media.ManifestFingerprint = ComputeManifestFingerprint(
		media.ContainerFormat, media.MediaSequenceBase, initMap, segments, keys)
	return media, nil
}

// SelectBestVariant picks the best muxed (embedded or no audio) video variant:
// highest BANDWIDTH first, then largest RESOLUTION area, then highest
// AVERAGE-BANDWIDTH, then earliest declaration order. When every video variant
// requires an external audio track the result is ErrHLSDemuxedAudioNotSupported.
func SelectBestVariant(master *MasterPlaylist) (*VariantStream, error) {
	if master == nil || len(master.Variants) == 0 {
		return nil, fmt.Errorf("%w: no variants to select", types.ErrHLSInvalidManifest)
	}
	var best *VariantStream
	for i := range master.Variants {
		v := &master.Variants[i]
		if v.DemuxedAudio {
			continue
		}
		if best == nil || variantBetter(v, best) {
			best = v
		}
	}
	if best == nil {
		return nil, types.ErrHLSDemuxedAudioNotSupported
	}
	return best, nil
}

// variantBetter implements the strict-weak ordering of SelectBestVariant.
func variantBetter(a, b *VariantStream) bool {
	if a.Bandwidth != b.Bandwidth {
		return a.Bandwidth > b.Bandwidth
	}
	aPixels, bPixels := int64(a.Width)*int64(a.Height), int64(b.Width)*int64(b.Height)
	if aPixels != bPixels {
		return aPixels > bPixels
	}
	if a.AverageBandwidth != b.AverageBandwidth {
		return a.AverageBandwidth > b.AverageBandwidth
	}
	return a.Order < b.Order
}

// cleanURIPathForFingerprint strips Scheme/Host/Query/Fragment and normalizes
// the remaining path so edge-host rotation and re-signed query strings do not
// change the manifest fingerprint.
func cleanURIPathForFingerprint(rawURI string) string {
	var p string
	if u, err := url.Parse(rawURI); err == nil {
		// EscapedPath keeps percent-encoded distinctions (%2F vs /) intact
		// and never emits raw '|' or newline characters, so serialized
		// records cannot be reframed by a crafted URI.
		p = u.EscapedPath()
		if p == "" {
			p = u.Path
		}
	}
	if p == "" {
		p = rawURI
		if i := strings.IndexAny(p, "?#"); i >= 0 {
			p = p[:i]
		}
		if i := strings.Index(p, "://"); i >= 0 {
			rest := p[i+3:]
			if j := strings.IndexByte(rest, '/'); j >= 0 {
				p = rest[j:]
			} else {
				p = ""
			}
		}
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}

// ComputeManifestFingerprint produces a deterministic sha256 over the
// normalized playlist structure. Serialization is strictly index-ordered
// (no map iteration) and all URIs contribute only their cleaned paths.
func ComputeManifestFingerprint(containerFormat string, mediaSequenceBase int64,
	initMap *MediaInitMap, segments []types.HLSSegmentState, keys []types.HLSKeyInfo,
) string {
	initPath, initOff, initLen := "", int64(0), int64(0)
	if initMap != nil {
		initPath = cleanURIPathForFingerprint(initMap.URI)
		initOff = initMap.ByteOffset
		initLen = initMap.ByteLength
	}
	h := sha256.New()
	fmt.Fprintf(h, "v2\n")
	fmt.Fprintf(h, "container=%s|total=%d|seq_base=%d|init_path=%q|init_off=%d|init_len=%d\n",
		containerFormat, len(segments), mediaSequenceBase, initPath, initOff, initLen)
	for _, seg := range segments {
		keyMethod := "NONE"
		if seg.KeyIndex > 0 && seg.KeyIndex <= len(keys) {
			keyMethod = keys[seg.KeyIndex-1].Method
		}
		fmt.Fprintf(h, "seg=%d|seq=%d|dur=%d|off=%d|len=%d|key=%s|path=%q\n",
			seg.Index, seg.Sequence, seg.DurationMicros, seg.ByteOffset, seg.ByteLength,
			keyMethod, cleanURIPathForFingerprint(seg.URI))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeEstimatedBytes estimates the byte size of the selected variant from
// its peak BANDWIDTH and the summed media duration. Unknown sizes yield 0.
func ComputeEstimatedBytes(bandwidthBitsPerSec int64, totalDurationSec float64) int64 {
	if bandwidthBitsPerSec <= 0 || totalDurationSec <= 0 {
		return 0
	}
	est := float64(bandwidthBitsPerSec) / 8.0 * totalDurationSec
	if math.IsNaN(est) || est <= 0 {
		return 0
	}
	if est >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(est)
}

// ComputeRecordEligible applies the speedstats eligibility rule: unknown size
// (<=0) stays eligible, known sizes require strictly more than
// types.RecordEligibleMinFileSize.
func ComputeRecordEligible(estimatedBytes int64) bool {
	if estimatedBytes <= 0 {
		return true
	}
	return estimatedBytes > types.RecordEligibleMinFileSize
}
