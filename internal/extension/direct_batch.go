package extension

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"goaria-v3/internal/rpc"
)

const (
	maxDirectBatchPayloadBytes     = 768 << 10
	maxDirectURLBytes              = 4096
	maxDirectFilenameBytes         = 1024
	maxDirectDownloadPageBytes     = 2048
	maxDirectOptionalFieldBytes    = 1024
	maxDirectHeadersPerItem        = 16
	maxDirectHeaderLineBytes       = 4096
	maxDirectHeaderBytesPerItem    = 16 << 10
	maxDirectHeaderBytesPerRequest = 256 << 10
)

var directBatchTopAllowlist = map[string]struct{}{
	"type":         {},
	"request_id":   {},
	"items":        {},
	"create_group": {},
	"folder_name":  {},
}

var directBatchItemAllowlist = map[string]struct{}{
	"client_item_id":  {},
	"url":             {},
	"final_url":       {},
	"headers":         {},
	"file_size":       {},
	"skip_head_probe": {},
	"filename":        {},
	"download_page":   {},
}

var directBatchStatusAllowlist = map[string]struct{}{
	"type":       {},
	"request_id": {},
}

var deniedDirectHeaderNames = map[string]struct{}{
	"host":              {},
	"connection":        {},
	"keep-alive":        {},
	"proxy-connection":  {},
	"transfer-encoding": {},
	"upgrade":           {},
	"te":                {},
	"trailer":           {},
	"content-length":    {},
	"range":             {},
	"authorization":     {},
}

// ParseDirectBatchRequest strict-allowlist-parses a download_batch payload.
// Failures are whole-request invalid_request with no side effects.
func ParseDirectBatchRequest(raw []byte) (DirectBatchRequest, string) {
	if len(raw) > maxDirectBatchPayloadBytes {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}
	top, ok := unmarshalObjectAllowlist(raw, directBatchTopAllowlist)
	if !ok {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}

	msgType, ok := requiredJSONString(top, "type")
	if !ok || msgType != MsgTypeDownloadBatch {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}
	requestID, ok := requiredJSONString(top, "request_id")
	if !ok {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}
	canonicalID, ok := canonicalRFC4122UUID(requestID)
	if !ok {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}

	itemsRaw, ok := top["items"]
	if !ok || isJSONNull(itemsRaw) {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}
	var itemRaws []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &itemRaws); err != nil {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}
	if len(itemRaws) == 0 || len(itemRaws) > MaxResolveSessionItems {
		return DirectBatchRequest{}, ErrCodeInvalidRequest
	}

	createGroup := false
	if rawCreate, present := top["create_group"]; present {
		if isJSONNull(rawCreate) {
			return DirectBatchRequest{}, ErrCodeInvalidRequest
		}
		if err := json.Unmarshal(rawCreate, &createGroup); err != nil {
			return DirectBatchRequest{}, ErrCodeInvalidRequest
		}
	}

	folderName := ""
	if rawFolder, present := top["folder_name"]; present {
		if isJSONNull(rawFolder) {
			return DirectBatchRequest{}, ErrCodeInvalidRequest
		}
		if err := json.Unmarshal(rawFolder, &folderName); err != nil {
			return DirectBatchRequest{}, ErrCodeInvalidRequest
		}
		if hasCRLFOrNUL(folderName) || len(folderName) > maxDirectOptionalFieldBytes {
			return DirectBatchRequest{}, ErrCodeInvalidRequest
		}
	}

	items := make([]DirectBatchItem, 0, len(itemRaws))
	seenIDs := make(map[string]struct{}, len(itemRaws))
	headerBytesTotal := 0
	for _, itemRaw := range itemRaws {
		item, itemHeaderBytes, errCode := parseDirectBatchItem(itemRaw)
		if errCode != "" {
			return DirectBatchRequest{}, errCode
		}
		if _, dup := seenIDs[item.ClientItemID]; dup {
			return DirectBatchRequest{}, ErrCodeInvalidRequest
		}
		seenIDs[item.ClientItemID] = struct{}{}
		headerBytesTotal += itemHeaderBytes
		if headerBytesTotal > maxDirectHeaderBytesPerRequest {
			return DirectBatchRequest{}, ErrCodeInvalidRequest
		}
		items = append(items, item)
	}

	return DirectBatchRequest{
		Type:        msgType,
		RequestID:   canonicalID,
		Items:       items,
		CreateGroup: createGroup,
		FolderName:  folderName,
	}, ""
}

// ParseDirectBatchStatusRequest strict-allowlist-parses download_batch_status.
func ParseDirectBatchStatusRequest(raw []byte) (string, string) {
	if len(raw) > maxDirectBatchPayloadBytes {
		return "", ErrCodeInvalidRequest
	}
	top, ok := unmarshalObjectAllowlist(raw, directBatchStatusAllowlist)
	if !ok {
		return "", ErrCodeInvalidRequest
	}
	msgType, ok := requiredJSONString(top, "type")
	if !ok || msgType != MsgTypeDownloadBatchStatus {
		return "", ErrCodeInvalidRequest
	}
	requestID, ok := requiredJSONString(top, "request_id")
	if !ok {
		return "", ErrCodeInvalidRequest
	}
	canonicalID, ok := canonicalRFC4122UUID(requestID)
	if !ok {
		return "", ErrCodeInvalidRequest
	}
	return canonicalID, ""
}

func parseDirectBatchItem(raw json.RawMessage) (DirectBatchItem, int, string) {
	fields, ok := unmarshalObjectAllowlist(raw, directBatchItemAllowlist)
	if !ok {
		return DirectBatchItem{}, 0, ErrCodeInvalidRequest
	}
	clientID, ok := requiredJSONString(fields, "client_item_id")
	if !ok || !validDirectClientItemID(clientID) {
		return DirectBatchItem{}, 0, ErrCodeInvalidRequest
	}
	rawURL, ok := requiredJSONString(fields, "url")
	if !ok {
		return DirectBatchItem{}, 0, ErrCodeInvalidRequest
	}
	canonical, errCode := canonicalizeDirectURL(rawURL, maxDirectURLBytes)
	if errCode != "" {
		return DirectBatchItem{}, 0, errCode
	}

	item := DirectBatchItem{
		ClientItemID: clientID,
		URL:          canonical,
		CanonicalURL: canonical,
	}

	if rawFinal, present := fields["final_url"]; present {
		if isJSONNull(rawFinal) {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		var finalURL string
		if err := json.Unmarshal(rawFinal, &finalURL); err != nil {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		canonFinal, errCode := canonicalizeDirectURL(finalURL, maxDirectURLBytes)
		if errCode != "" {
			return DirectBatchItem{}, 0, errCode
		}
		item.FinalURL = canonFinal
	}

	headerBytes := 0
	if rawHeaders, present := fields["headers"]; present {
		if isJSONNull(rawHeaders) {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		var lines []string
		if err := json.Unmarshal(rawHeaders, &lines); err != nil {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		normalized, nbytes, errCode := normalizeDirectHeaders(lines)
		if errCode != "" {
			return DirectBatchItem{}, 0, errCode
		}
		item.Headers = normalized
		headerBytes = nbytes
	}

	if rawSize, present := fields["file_size"]; present {
		n, errCode := parseNonNegativeJSONInt(rawSize)
		if errCode != "" {
			return DirectBatchItem{}, 0, errCode
		}
		item.FileSize = n
		item.HasFileSize = true
	}

	if rawSkip, present := fields["skip_head_probe"]; present {
		if isJSONNull(rawSkip) {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		var skip bool
		if err := json.Unmarshal(rawSkip, &skip); err != nil {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		item.SkipHeadProbe = skip
	}

	if rawName, present := fields["filename"]; present {
		if isJSONNull(rawName) {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		var name string
		if err := json.Unmarshal(rawName, &name); err != nil {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		if hasCRLFOrNUL(name) || len(name) > maxDirectFilenameBytes {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		item.Filename = name
	}

	if rawPage, present := fields["download_page"]; present {
		if isJSONNull(rawPage) {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		var page string
		if err := json.Unmarshal(rawPage, &page); err != nil {
			return DirectBatchItem{}, 0, ErrCodeInvalidRequest
		}
		canonPage, errCode := canonicalizeDirectURL(page, maxDirectDownloadPageBytes)
		if errCode != "" {
			return DirectBatchItem{}, 0, errCode
		}
		item.DownloadPage = canonPage
	}

	return item, headerBytes, ""
}

func normalizeDirectHeaders(lines []string) ([]string, int, string) {
	if len(lines) > maxDirectHeadersPerItem {
		return nil, 0, ErrCodeInvalidRequest
	}
	out := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	total := 0
	for _, line := range lines {
		if len(line) > maxDirectHeaderLineBytes {
			return nil, 0, ErrCodeInvalidRequest
		}
		if strings.ContainsAny(line, "\r\n\x00") {
			return nil, 0, ErrCodeInvalidRequest
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return nil, 0, ErrCodeInvalidRequest
		}
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, 0, ErrCodeInvalidRequest
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			return nil, 0, ErrCodeInvalidRequest
		}
		if strings.ContainsAny(name, "\r\n\x00") || strings.ContainsAny(value, "\r\n\x00") {
			return nil, 0, ErrCodeInvalidRequest
		}
		lower := strings.ToLower(name)
		if _, denied := deniedDirectHeaderNames[lower]; denied || strings.HasPrefix(lower, "proxy-") {
			return nil, 0, ErrCodeInvalidRequest
		}
		if _, dup := seen[lower]; dup {
			return nil, 0, ErrCodeInvalidRequest
		}
		seen[lower] = struct{}{}
		normalized := name + ": " + value
		total += len(normalized)
		if total > maxDirectHeaderBytesPerItem {
			return nil, 0, ErrCodeInvalidRequest
		}
		out = append(out, normalized)
	}
	if err := rpc.ValidateAddURIHeaders(out); err != nil {
		return nil, 0, ErrCodeInvalidRequest
	}
	return out, total, ""
}

func canonicalizeDirectURL(raw string, maxBytes int) (string, string) {
	if raw == "" || len(raw) > maxBytes {
		return "", ErrCodeInvalidRequest
	}
	if hasCRLFOrNUL(raw) {
		return "", ErrCodeInvalidRequest
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", ErrCodeInvalidRequest
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() {
		return "", ErrCodeInvalidRequest
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrCodeInvalidRequest
	}
	if parsed.User != nil {
		return "", ErrCodeInvalidRequest
	}
	host := strings.ToLower(parsed.Host)
	if host == "" {
		return "", ErrCodeInvalidRequest
	}
	path := parsed.EscapedPath()
	if parsed.RawPath != "" {
		path = parsed.RawPath
	}
	var b strings.Builder
	b.Grow(len(scheme) + 3 + len(host) + len(path) + 1 + len(parsed.RawQuery))
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(host)
	b.WriteString(path)
	if parsed.RawQuery != "" {
		b.WriteByte('?')
		b.WriteString(parsed.RawQuery)
	}
	out := b.String()
	if len(out) > maxBytes {
		return "", ErrCodeInvalidRequest
	}
	return out, ""
}

func unmarshalObjectAllowlist(raw json.RawMessage, allowlist map[string]struct{}) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false
	}
	if fields == nil {
		return nil, false
	}
	for key := range fields {
		if _, ok := allowlist[key]; !ok {
			return nil, false
		}
	}
	return fields, true
}

func requiredJSONString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok || isJSONNull(raw) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func parseNonNegativeJSONInt(raw json.RawMessage) (int64, string) {
	if isJSONNull(raw) {
		return 0, ErrCodeInvalidRequest
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, ErrCodeInvalidRequest
	}
	if trimmed[0] == '"' || bytes.ContainsAny(trimmed, ".eE") {
		return 0, ErrCodeInvalidRequest
	}
	n, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil || n < 0 {
		return 0, ErrCodeInvalidRequest
	}
	return n, ""
}

func canonicalRFC4122UUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	for i := range 36 {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", false
			}
		default:
			if !isHexDigit(c) {
				return "", false
			}
		}
	}
	return strings.ToLower(s), true
}

func validDirectClientItemID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := range 32 {
		c := id[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func hasCRLFOrNUL(value string) bool {
	return strings.ContainsAny(value, "\r\n\x00")
}
