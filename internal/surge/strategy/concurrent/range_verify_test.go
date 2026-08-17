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

func TestParse416StarTotal(t *testing.T) {
	n, err := parse416StarTotal("bytes */8192")
	if err != nil || n != 8192 {
		t.Fatalf("got %d, %v", n, err)
	}
	if _, err := parse416StarTotal("BYTES */100"); err != nil {
		t.Fatalf("case-insensitive prefix: %v", err)
	}
	rejects := []string{"", "bytes 0-1/100", "bytes */*", "bytes */0", "bytes 0-99/100", "bytes 0-10,11-20/100"}
	for _, h := range rejects {
		if _, err := parse416StarTotal(h); err == nil {
			t.Fatalf("parse416StarTotal(%q) must fail", h)
		}
	}
}

func TestClassifyPayloadFirstHeaders(t *testing.T) {
	task := types.Task{Offset: 0, Length: 1024}
	cases := []struct {
		name     string
		resp     *http.Response
		want     error
		kind     types.PayloadFirstMismatchKind
		observed int64
		typed    bool
	}{
		{
			name: "valid_206",
			resp: &http.Response{
				StatusCode:    http.StatusPartialContent,
				ContentLength: 1024,
				Header:        http.Header{"Content-Range": []string{"bytes 0-1023/4096"}, "Content-Length": []string{"1024"}},
			},
		},
		{
			name: "shorter_legal_206",
			resp: &http.Response{
				StatusCode:    http.StatusPartialContent,
				ContentLength: 512,
				Header:        http.Header{"Content-Range": []string{"bytes 0-511/4096"}, "Content-Length": []string{"512"}},
			},
		},
		{
			name: "200_matching_size",
			resp: &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: 4096,
				Header:        http.Header{"Content-Length": []string{"4096"}},
			},
			want: types.ErrRangeUnsupported,
		},
		{
			name: "200_no_content_length",
			resp: &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: -1,
				Header:        http.Header{},
			},
			want:  types.ErrSourceMetadataMismatch,
			kind:  types.MismatchKind200Chunked,
			typed: true,
		},
		{
			name: "200_wrong_content_length",
			resp: &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: 12,
				Header:        http.Header{"Content-Length": []string{"12"}},
			},
			want:     types.ErrSourceMetadataMismatch,
			kind:     types.MismatchKind200CL,
			observed: 12,
			typed:    true,
		},
		{
			name:  "416",
			resp:  &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Header: http.Header{}},
			want:  types.ErrSourceMetadataMismatch,
			kind:  types.MismatchKind416Bare,
			typed: true,
		},
		{
			name: "416_star_total",
			resp: &http.Response{
				StatusCode: http.StatusRequestedRangeNotSatisfiable,
				Header:     http.Header{"Content-Range": []string{"bytes */8192"}},
			},
			want:     types.ErrSourceMetadataMismatch,
			kind:     types.MismatchKind416StarTotal,
			observed: 8192,
			typed:    true,
		},
		{
			name: "403_legacy",
			resp: &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}},
			want: errPayloadFirstLegacyStatus,
		},
		{
			name: "429_legacy",
			resp: &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}},
			want: errPayloadFirstLegacyStatus,
		},
		{
			name: "multiple_content_range",
			resp: &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{"bytes 0-1023/4096", "bytes 0-1023/4096"}},
			},
			want:  types.ErrSourceMetadataMismatch,
			kind:  types.MismatchKindMultipart,
			typed: true,
		},
		{
			name: "206_wrong_end",
			resp: &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{"bytes 0-2047/4096"}, "Content-Length": []string{"2048"}},
			},
			want: types.ErrSourceMetadataMismatch,
		},
		{
			name: "206_wrong_total",
			resp: &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{"bytes 0-1023/9999"}, "Content-Length": []string{"1024"}},
			},
			want:     types.ErrSourceMetadataMismatch,
			kind:     types.MismatchKind206Total,
			observed: 9999,
			typed:    true,
		},
		{
			name: "206_star",
			resp: &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{"bytes 0-1023/*"}, "Content-Length": []string{"1024"}},
			},
			want:  types.ErrSourceMetadataMismatch,
			kind:  types.MismatchKind206Star,
			typed: true,
		},
		{
			name: "206_start_mismatch_and_different_total",
			resp: &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{"bytes 100-1023/9999"}, "Content-Length": []string{"924"}},
			},
			want: types.ErrSourceMetadataMismatch,
		},
		{
			name: "206_cl_mismatch_and_different_total",
			resp: &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{"bytes 0-1023/9999"}, "Content-Length": []string{"12"}},
			},
			want: types.ErrSourceMetadataMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyPayloadFirstHeaders(tc.resp, task, 4096)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			var mismatch *types.SourceMetadataMismatchError
			hasTyped := errors.As(err, &mismatch)
			if tc.typed {
				if !hasTyped {
					t.Fatal("expected typed mismatch wrapper")
				}
				if mismatch.Kind != tc.kind {
					t.Fatalf("Kind = %q, want %q", mismatch.Kind, tc.kind)
				}
				if mismatch.ObservedSize != tc.observed {
					t.Fatalf("ObservedSize = %d, want %d", mismatch.ObservedSize, tc.observed)
				}
				return
			}
			if hasTyped && mismatch.Kind == types.MismatchKind206Total {
				t.Fatalf("Kind must not be 206_total, got %+v", mismatch)
			}
		})
	}
}
