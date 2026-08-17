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

func mismatchErr(kind types.PayloadFirstMismatchKind, observed int64) error {
	return types.NewSourceMetadataMismatch(kind, observed)
}

// contentRangeTotalIsStar reports a 206 total=* without teaching
// parseSingleContentRange to accept star totals.
func contentRangeTotalIsStar(header string) bool {
	h := strings.TrimSpace(header)
	lower := strings.ToLower(h)
	if !strings.HasPrefix(lower, "bytes ") {
		return false
	}
	rest := strings.TrimSpace(h[6:])
	slash := strings.LastIndex(rest, "/")
	if slash < 0 || slash+1 >= len(rest) {
		return false
	}
	return rest[slash+1:] == "*"
}

// parse416StarTotal accepts 416 Content-Range "bytes */N" with N>0.
// FORK-PATCH: used only from the HTTP 416 branch; never from validatePayloadFirst206.
func parse416StarTotal(header string) (total int64, err error) {
	h := strings.TrimSpace(header)
	if h == "" {
		return 0, fmt.Errorf("missing Content-Range")
	}
	if strings.Contains(h, ",") {
		return 0, fmt.Errorf("multipart Content-Range")
	}
	lower := strings.ToLower(h)
	if !strings.HasPrefix(lower, "bytes ") {
		return 0, fmt.Errorf("invalid Content-Range unit")
	}
	rest := strings.TrimSpace(h[6:])
	if !strings.HasPrefix(rest, "*/") {
		return 0, fmt.Errorf("invalid 416 Content-Range")
	}
	totalStr := rest[2:]
	if totalStr == "*" {
		return 0, fmt.Errorf("unknown Content-Range total")
	}
	total, err = strconv.ParseInt(totalStr, 10, 64)
	if err != nil || total <= 0 {
		return 0, fmt.Errorf("invalid Content-Range total")
	}
	return total, nil
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
		if cl < 0 {
			return mismatchErr(types.MismatchKind200Chunked, 0)
		}
		if cl > 0 {
			return mismatchErr(types.MismatchKind200CL, cl)
		}
		return types.ErrSourceMetadataMismatch
	case http.StatusRequestedRangeNotSatisfiable:
		n, err := parse416StarTotal(resp.Header.Get("Content-Range"))
		if err == nil {
			return mismatchErr(types.MismatchKind416StarTotal, n)
		}
		return mismatchErr(types.MismatchKind416Bare, 0)
	default:
		return errPayloadFirstLegacyStatus
	}
}

func validatePayloadFirst206(resp *http.Response, task types.Task, trustedSize int64) error {
	values := resp.Header.Values("Content-Range")
	if len(values) != 1 {
		if len(values) > 1 {
			return mismatchErr(types.MismatchKindMultipart, 0)
		}
		return types.ErrSourceMetadataMismatch
	}
	start, end, total, err := parseSingleContentRange(values[0])
	if err != nil {
		if contentRangeTotalIsStar(values[0]) {
			return mismatchErr(types.MismatchKind206Star, 0)
		}
		return types.ErrSourceMetadataMismatch
	}
	wantEnd := task.Offset + task.Length - 1
	spanOK := start == task.Offset && end >= start && end <= wantEnd
	served := int64(0)
	if end >= start {
		served = end - start + 1
	}
	clOK := true
	if raw := strings.TrimSpace(resp.Header.Get("Content-Length")); raw != "" {
		cl, clErr := strconv.ParseInt(raw, 10, 64)
		if clErr != nil || cl != served {
			clOK = false
		}
	}
	if spanOK && clOK && total > 0 && total != trustedSize {
		return mismatchErr(types.MismatchKind206Total, total)
	}
	if !spanOK || !clOK || total != trustedSize {
		return types.ErrSourceMetadataMismatch
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
