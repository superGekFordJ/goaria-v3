package concurrent

import (
	"errors"
	"net/http"
	"testing"

	"goaria-v3/internal/surge/types"
)

func TestParseSingleContentRange(t *testing.T) {
	start, end, total, err := parseSingleContentRange("bytes 0-1023/4096")
	if err != nil {
		t.Fatal(err)
	}
	if start != 0 || end != 1023 || total != 4096 {
		t.Fatalf("got %d-%d/%d", start, end, total)
	}
	if _, _, _, err := parseSingleContentRange("bytes 0-1023/*"); err == nil {
		t.Fatal("star total must fail")
	}
	if _, _, _, err := parseSingleContentRange("bytes 0-10,11-20/100"); err == nil {
		t.Fatal("multipart must fail")
	}
}

func TestClassifyPayloadFirstHeaders(t *testing.T) {
	task := types.Task{Offset: 0, Length: 1024}
	resp206 := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 1024,
		Header:        http.Header{"Content-Range": []string{"bytes 0-1023/4096"}, "Content-Length": []string{"1024"}},
	}
	if err := classifyPayloadFirstHeaders(resp206, task, 4096); err != nil {
		t.Fatalf("valid 206: %v", err)
	}

	resp200 := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: 4096,
		Header:        http.Header{"Content-Length": []string{"4096"}},
	}
	if !errors.Is(classifyPayloadFirstHeaders(resp200, task, 4096), types.ErrRangeUnsupported) {
		t.Fatal("200+matching CL must be ErrRangeUnsupported")
	}

	resp416 := &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Header: http.Header{}}
	if !errors.Is(classifyPayloadFirstHeaders(resp416, task, 4096), types.ErrSourceMetadataMismatch) {
		t.Fatal("416 must be mismatch")
	}

	resp403 := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	if !errors.Is(classifyPayloadFirstHeaders(resp403, task, 4096), errPayloadFirstLegacyStatus) {
		t.Fatal("403 must use legacy status handling")
	}
}
