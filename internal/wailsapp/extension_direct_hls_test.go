package wailsapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/strategy/hls"
	surgetypes "goaria-v3/internal/surge/types"
)

// hlsDirectSurgeEngine plays the Surge half of a HybridEngine in adapter
// tests: it claims Surge activity and captures the submitted options.
type hlsDirectSurgeEngine struct {
	recordingDirectEngine
	optMu   sync.Mutex
	options []rpc.AddURIOptions
}

func (e *hlsDirectSurgeEngine) AddUri(url string, opts rpc.AddURIOptions) (string, error) {
	e.optMu.Lock()
	e.options = append(e.options, opts)
	e.optMu.Unlock()
	return e.recordingDirectEngine.AddUri(url, opts)
}

func (e *hlsDirectSurgeEngine) IsSurgeActive() bool { return true }

func (e *hlsDirectSurgeEngine) snapshotOptions() []rpc.AddURIOptions {
	e.optMu.Lock()
	defer e.optMu.Unlock()
	out := make([]rpc.AddURIOptions, len(e.options))
	copy(out, e.options)
	return out
}

func setupDirectHLSAdapter(t *testing.T) (*directBatchAdapter, *recordingDirectEngine, *hlsDirectSurgeEngine) {
	t.Helper()
	originalConfig := config.Get()
	originalSave := history.SaveEnabled
	history.DisableSaveForTest()
	history.Clear()
	monitor.ResetDownloadGroupNamerForTest()
	monitor.ResetTaskGroupStoreForTest(t.TempDir()+"/download_groups.json", true)
	config.SetTestConfig(&config.AppConfig{
		DownloadDir:     t.TempDir(),
		SmartThreadMode: false,
		MaxConnections:  "8",
	})
	t.Cleanup(func() {
		history.Clear()
		history.SetSaveEnabled(originalSave)
		monitor.ResetDownloadGroupNamerForTest()
		monitor.ResetTaskGroupStoreForTest("", true)
		config.SetTestConfig(originalConfig)
	})
	aria2 := &recordingDirectEngine{}
	surge := &hlsDirectSurgeEngine{}
	app := NewApp(Options{DownloadEngine: rpc.NewHybridEngine(aria2, surge)})
	return newDirectBatchAdapter(app), aria2, surge
}

const hlsAdapterVODManifest = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:10.0,
seg0.ts
#EXT-X-ENDLIST
`

// Parsed wire → adapter → PreparedDirectAddItem → candidate → HLS naming:
// the page title must survive the whole chain and show up in the Surge
// options' Out name, with zero Aria2 involvement.
func TestDirectBatchAdapter_HLSPageTitleNamesOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(hlsAdapterVODManifest))
	}))
	defer srv.Close()

	adapter, aria2, surge := setupDirectHLSAdapter(t)
	manifestURL := srv.URL + "/index.m3u8"

	raw := `{"type":"download_batch","request_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","items":[` +
		`{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"` + manifestURL + `","page_title":"Live Show"},` +
		`{"client_item_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","url":"` + srv.URL + `/b.bin","filename":"b.bin"}` +
		`]}`
	req, code := extension.ParseDirectBatchRequest([]byte(raw))
	if code != "" {
		t.Fatalf("ParseDirectBatchRequest: %s", code)
	}

	result := adapter.HandleDirectBatch(context.Background(),
		extension.RequestEnvelope{Type: extension.MsgTypeDownloadBatch, RequestID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		req)
	if result.ErrorCode != "" || !result.Success {
		t.Fatalf("commit result = %+v", result)
	}
	if len(result.SucceededItemIDs) != 2 {
		t.Fatalf("succeeded = %v, want both items", result.SucceededItemIDs)
	}
	if got := aria2.callCount(); got != 0 {
		t.Fatalf("aria2 AddUri calls = %d, want 0", got)
	}

	opts := surge.snapshotOptions()
	if len(opts) != 2 {
		t.Fatalf("surge AddUri calls = %d, want 2", len(opts))
	}
	var hlsOpts, binOpts *rpc.AddURIOptions
	for i := range opts {
		if opts[i].Strategy == surgetypes.StrategyHLS {
			hlsOpts = &opts[i]
		} else {
			binOpts = &opts[i]
		}
	}
	if hlsOpts == nil || hlsOpts.HLSResume == nil {
		t.Fatalf("no HLS submission in %+v", opts)
	}
	want := hls.ResolveHLSOutputFilename(manifestURL, "", "Live Show", hls.ContainerTS, time.Now())
	if hlsOpts.Out != want {
		t.Fatalf("HLS Out = %q, want %q (page title must win over index.m3u8)", hlsOpts.Out, want)
	}
	if !strings.HasSuffix(hlsOpts.Out, ".ts") {
		t.Fatalf("HLS Out = %q, want .ts", hlsOpts.Out)
	}
	if binOpts == nil {
		t.Fatal("ordinary item missing from surge submissions")
	}
	if binOpts.Out != "b.bin" || binOpts.HLSResume != nil {
		t.Fatalf("ordinary item disturbed by sibling title: %+v", binOpts)
	}
	if gids := surge.urls; len(gids) != 2 {
		t.Fatalf("recorded urls = %v", gids)
	}
}
