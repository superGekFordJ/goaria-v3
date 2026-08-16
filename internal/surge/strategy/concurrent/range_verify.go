package concurrent

// FORK-PATCH: payload-first Range header contract (206 + Content-Range) before body IO.

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"goaria-v3/internal/surge/types"
)

var errPayloadFirstLegacyStatus = fmt.Errorf("payload-first: use legacy status handling")

type limitedReadCloser struct {
	io.Reader
	io.Closer
}

func limitResponseBody(resp *http.Response, n int64) {
	if resp == nil || resp.Body == nil {
		return
	}
	resp.Body = limitedReadCloser{Reader: io.LimitReader(resp.Body, n), Closer: resp.Body}
}

func parseSingleContentRange(header string) (start, end, total int64, err error) {
	h := strings.TrimSpace(header)
	if h == "" {
		return 0, 0, 0, fmt.Errorf("missing Content-Range")
	}
	if strings.Contains(h, ",") {
		return 0, 0, 0, fmt.Errorf("multipart Content-Range")
	}
	lower := strings.ToLower(h)
	if !strings.HasPrefix(lower, "bytes ") {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range unit")
	}
	rest := strings.TrimSpace(h[6:])
	slash := strings.LastIndex(rest, "/")
	if slash <= 0 || slash+1 >= len(rest) {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range")
	}
	totalStr := rest[slash+1:]
	if totalStr == "*" {
		return 0, 0, 0, fmt.Errorf("unknown Content-Range total")
	}
	total, err = strconv.ParseInt(totalStr, 10, 64)
	if err != nil || total <= 0 {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range total")
	}
	rangePart := rest[:slash]
	dash := strings.IndexByte(rangePart, '-')
	if dash < 0 {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range span")
	}
	start, err = strconv.ParseInt(rangePart[:dash], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range start")
	}
	end, err = strconv.ParseInt(rangePart[dash+1:], 10, 64)
	if err != nil || end < start {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range end")
	}
	return start, end, total, nil
}

// classifyPayloadFirstHeaders enforces the first-shard Range contract before any
// body read. 200 is never Range proof. 403/429/5xx return errPayloadFirstLegacyStatus
// so existing worker retry / sticky-403 paths stay in charge.
func classifyPayloadFirstHeaders(resp *http.Response, task types.Task, trustedSize int64) error {
	if resp == nil {
		return types.ErrSourceMetadataMismatch
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		return validatePayloadFirst206(resp, task, trustedSize)
	case http.StatusOK:
		cl := resp.ContentLength
		if cl < 0 {
			if raw := strings.TrimSpace(resp.Header.Get("Content-Length")); raw != "" {
				parsed, err := strconv.ParseInt(raw, 10, 64)
				if err != nil {
					return types.ErrSourceMetadataMismatch
				}
				cl = parsed
			}
		}
		if cl == trustedSize && trustedSize > 0 {
			return types.ErrRangeUnsupported
		}
		return types.ErrSourceMetadataMismatch
	case http.StatusRequestedRangeNotSatisfiable:
		return types.ErrSourceMetadataMismatch
	default:
		return errPayloadFirstLegacyStatus
	}
}

func validatePayloadFirst206(resp *http.Response, task types.Task, trustedSize int64) error {
	values := resp.Header.Values("Content-Range")
	if len(values) != 1 {
		return types.ErrSourceMetadataMismatch
	}
	start, end, total, err := parseSingleContentRange(values[0])
	if err != nil {
		return types.ErrSourceMetadataMismatch
	}
	wantEnd := task.Offset + task.Length - 1
	if start != task.Offset || end < start || end > wantEnd || total != trustedSize {
		return types.ErrSourceMetadataMismatch
	}
	served := end - start + 1
	if raw := strings.TrimSpace(resp.Header.Get("Content-Length")); raw != "" {
		cl, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || cl != served {
			return types.ErrSourceMetadataMismatch
		}
	}
	return nil
}

func payloadFirstServedLength(resp *http.Response, task types.Task) int64 {
	if resp == nil {
		return task.Length
	}
	values := resp.Header.Values("Content-Range")
	if len(values) != 1 {
		return task.Length
	}
	start, end, _, err := parseSingleContentRange(values[0])
	if err != nil || end < start {
		return task.Length
	}
	return end - start + 1
}
