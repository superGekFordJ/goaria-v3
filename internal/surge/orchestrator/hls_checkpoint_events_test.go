package orchestrator

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// hlsSpillCheckpoint builds a strict-valid checkpoint carrying out-of-order
// spill claims: two merged segments plus one spill entry per spillLen, with
// the ledger, bitmap bits and VerifiedBytes == MergedBytes + Σ lens.
func hlsSpillCheckpoint(url string, merged int64, spillLens ...int64) *types.HLSResumeState {
	const total = 4
	const mergedSegs = 2
	segs := make([]types.HLSSegmentState, total)
	for i := range segs {
		segs[i] = types.HLSSegmentState{Index: i}
	}
	entries := make([]types.HLSSpillEntry, len(spillLens))
	var off, sum int64
	var bitmap byte = 0x03 // merged prefix bits 0,1
	for i, l := range spillLens {
		idx := mergedSegs + i
		entries[i] = types.HLSSpillEntry{SegmentIndex: idx, Offset: off, Length: l}
		bitmap |= 1 << uint(idx)
		off += l
		sum += l
	}
	return &types.HLSResumeState{
		ManifestURL:      url,
		MediaPlaylistURL: url,
		TotalSegments:    total,
		CompletedBitmap:  []byte{bitmap},
		MergedSegments:   mergedSegs,
		MergedBytes:      merged,
		VerifiedBytes:    merged + sum,
		Segments:         segs,
		SpillEntries:     entries,
		SpillPath:        url + ".spill",
		SpillFileSize:    sum,
	}
}

// hlsZeroCheckpoint is the strict-valid empty ledger: every bit unset, no
// merged bytes — the first-pause shape that still proves checkpoint authority.
func hlsZeroCheckpoint(url string) *types.HLSResumeState {
	segs := make([]types.HLSSegmentState, 4)
	for i := range segs {
		segs[i] = types.HLSSegmentState{Index: i}
	}
	return &types.HLSResumeState{
		ManifestURL:      url,
		MediaPlaylistURL: url,
		TotalSegments:    4,
		CompletedBitmap:  []byte{0x00},
		MergedSegments:   0,
		MergedBytes:      0,
		VerifiedBytes:    0,
		Segments:         segs,
	}
}

// corruptHLSCheckpoint returns a checkpoint that fails
// ValidateCheckpointSeed: a bitmap bit beyond the merged prefix with no spill
// entry to back it.
func corruptHLSCheckpoint(url string, verified int64) *types.HLSResumeState {
	hr := hlsCheckpoint(url, verified)
	hr.CompletedBitmap = []byte{0x0F}
	return hr
}

func TestHLSCheckpointEvents_Paused(t *testing.T) {
	runHLSCheckpointEventMatrix(t, types.EventPaused, "paused")
}

func TestHLSCheckpointEvents_Error(t *testing.T) {
	runHLSCheckpointEventMatrix(t, types.EventError, "error")
}

// runHLSCheckpointEventMatrix exercises the shared normalize seam through
// both lifecycle events: every case asserts master and detail agree on the
// checkpoint's VerifiedBytes, the detail snapshot still satisfies the
// resume-authority matcher, the event's State object is never mutated in
// place, and a snapshot with no provable checkpoint never overwrites the
// detail gob.
func runHLSCheckpointEventMatrix(t *testing.T, evType types.EventType, wantStatus string) {
	t.Helper()

	type seedCase struct {
		master           bool
		masterDownloaded int64
		detail           *types.HLSResumeState
		detailDownloaded int64
		detailFor        string // seed under a different task ID
	}

	cases := []struct {
		name               string
		seed               seedCase
		eventState         func(url, dest string) *types.DownloadRecord
		wantMaster         int64
		wantDetailV        int64 // -1 → expect no valid checkpoint in detail
		wantDetailD        int64
		wantDetailSame     bool // detail gob content must equal the seed
		wantMasterStrategy types.DownloadStrategy
	}{
		{
			name: "lying_downloaded_above_V_pins_to_V",
			seed: seedCase{master: true, masterDownloaded: 700, detail: hlsCheckpoint("", 500), detailDownloaded: 500},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 9999, HLSResume: hlsCheckpoint(url, 500),
				}
			},
			wantMaster:         500,
			wantDetailV:        500,
			wantDetailD:        500,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			name: "lying_downloaded_below_V_pins_to_V",
			seed: seedCase{master: true, masterDownloaded: 700, detail: hlsCheckpoint("", 500), detailDownloaded: 500},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 100, HLSResume: hlsCheckpoint(url, 500),
				}
			},
			wantMaster:         500,
			wantDetailV:        500,
			wantDetailD:        500,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			name: "valid_zero_checkpoint_regresses_to_zero",
			seed: seedCase{master: true, masterDownloaded: 700},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 300, HLSResume: hlsZeroCheckpoint(url),
				}
			},
			wantMaster:         0,
			wantDetailV:        0,
			wantDetailD:        0,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			name: "spill_ledger_checkpoint_pins_merged_plus_spill",
			seed: seedCase{master: true, masterDownloaded: 700},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 10, HLSResume: hlsSpillCheckpoint(url, 200, 50, 50),
				}
			},
			wantMaster:         300,
			wantDetailV:        300,
			wantDetailD:        300,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			name: "strategy_omitted_still_hls_classified",
			seed: seedCase{master: true, masterDownloaded: 700},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest,
					Downloaded: 9999, HLSResume: hlsCheckpoint(url, 500),
				}
			},
			wantMaster:         500,
			wantDetailV:        500,
			wantDetailD:        500,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			name: "invalid_event_falls_back_to_disk_checkpoint",
			seed: seedCase{master: true, masterDownloaded: 700, detail: hlsCheckpoint("", 500), detailDownloaded: 500},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 8888, HLSResume: corruptHLSCheckpoint(url, 500),
				}
			},
			wantMaster:         500,
			wantDetailV:        500,
			wantDetailD:        500,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			// The detail seed's SaveStateWithOptions syncs the master row —
			// 777 is the last-persisted figure; the dead snapshot must not
			// regress it or rewrite the gob.
			name: "both_invalid_never_overwrites_detail",
			seed: seedCase{master: true, masterDownloaded: 700, detail: corruptHLSCheckpoint("", 500), detailDownloaded: 777},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 42, HLSResume: corruptHLSCheckpoint(url, 500),
				}
			},
			wantMaster:         777,
			wantDetailV:        -1,
			wantDetailD:        777,
			wantDetailSame:     true,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			name: "master_missing_writes_master_and_detail",
			seed: seedCase{},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 12, HLSResume: hlsCheckpoint(url, 500),
				}
			},
			wantMaster:         500,
			wantDetailV:        500,
			wantDetailD:        500,
			wantMasterStrategy: types.StrategyHLS,
		},
		{
			name: "foreign_id_checkpoint_never_adopted",
			seed: seedCase{
				master: true, masterDownloaded: 700,
				detail: hlsCheckpoint("", 500), detailDownloaded: 500, detailFor: "foreign-id",
			},
			eventState: func(url, dest string) *types.DownloadRecord {
				return &types.DownloadRecord{
					URL: url, DestPath: dest, Strategy: types.StrategyHLS,
					Downloaded: 42, HLSResume: corruptHLSCheckpoint(url, 500),
				}
			},
			wantMaster:         700,
			wantDetailV:        -1,
			wantDetailD:        -1, // no detail for this task at all
			wantMasterStrategy: types.StrategyHLS,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := testutil.SetupStateDB(t)
			destPath := filepath.Join(tmpDir, "chk.ts")
			url := "https://hls.example.com/chk.m3u8"
			id := "chk-ev"

			if tc.seed.master {
				testutil.SeedMasterList(t, types.DownloadRecord{
					ID: id, URL: url, URLHash: store.URLHash(url),
					DestPath: destPath, Filename: "chk.ts",
					Status: "downloading", Downloaded: tc.seed.masterDownloaded,
					Strategy: types.StrategyHLS,
				})
			}
			var seededHR *types.HLSResumeState
			if tc.seed.detail != nil {
				seedID := id
				if tc.seed.detailFor != "" {
					seedID = tc.seed.detailFor
				}
				d := *tc.seed.detail
				if d.ManifestURL == "" {
					d.ManifestURL = url
					d.MediaPlaylistURL = url
				}
				seededHR = &d
				seedHLSDetail(t, seedID, url, destPath, "chk.ts", tc.seed.detailDownloaded, &d)
			}

			sent := tc.eventState(url, destPath)
			var sentHRBefore *types.HLSResumeState
			var sentHRClone *types.HLSResumeState
			if sent.HLSResume != nil {
				sentHRBefore = sent.HLSResume
				sentHRClone = sent.HLSResume.Clone()
			}
			sentDownloaded := sent.Downloaded

			ch := make(chan types.DownloadEvent, 4)
			mgr := NewLifecycleManager(nil, nil, nil)
			defer mgr.Shutdown()
			done := make(chan struct{})
			go func() {
				mgr.StartEventWorker(ch)
				close(done)
			}()

			ev := types.DownloadEvent{
				Type:       evType,
				DownloadID: id,
				Filename:   "chk.ts",
				DestPath:   destPath,
				URL:        url,
				Downloaded: sent.Downloaded,
				State:      sent,
			}
			if evType == types.EventError {
				ev.Err = errors.New("network reset")
			}
			ch <- ev
			close(ch)
			<-done

			entry, err := store.GetDownload(id)
			if err != nil || entry == nil {
				t.Fatalf("GetDownload: %v (nil=%v)", err, entry == nil)
			}
			if entry.Downloaded != tc.wantMaster {
				t.Fatalf("master Downloaded = %d, want %d", entry.Downloaded, tc.wantMaster)
			}
			if entry.Status != wantStatus {
				t.Fatalf("master Status = %q, want %q", entry.Status, wantStatus)
			}
			if tc.wantMasterStrategy != "" && entry.Strategy != tc.wantMasterStrategy {
				t.Fatalf("master Strategy = %q, want %q", entry.Strategy, tc.wantMasterStrategy)
			}

			details, err := store.LoadStates([]string{id})
			if err != nil {
				t.Fatalf("LoadStates: %v", err)
			}
			saved := details[id]
			switch {
			case tc.wantDetailD < 0:
				if saved != nil {
					t.Fatalf("detail written for task with no valid checkpoint: D=%d", saved.Downloaded)
				}
			case tc.wantDetailSame:
				if saved == nil {
					t.Fatal("seeded detail vanished")
				}
				if saved.Downloaded != tc.wantDetailD {
					t.Fatalf("detail Downloaded = %d, want seeded %d", saved.Downloaded, tc.wantDetailD)
				}
				if !reflect.DeepEqual(saved.HLSResume, seededHR) {
					t.Fatalf("detail checkpoint mutated: %+v", saved.HLSResume)
				}
			default:
				if saved == nil || saved.HLSResume == nil {
					t.Fatal("detail checkpoint missing")
				}
				if saved.HLSResume.VerifiedBytes != tc.wantDetailV {
					t.Fatalf("detail V = %d, want %d", saved.HLSResume.VerifiedBytes, tc.wantDetailV)
				}
				if saved.Downloaded != tc.wantDetailD {
					t.Fatalf("detail Downloaded = %d, want %d", saved.Downloaded, tc.wantDetailD)
				}
				if !isTaskBackedResumeSnapshot(*saved) {
					t.Fatal("persisted detail no longer satisfies resume-authority matcher")
				}
			}

			// The consumer must never mutate the event's State object in
			// place — snapshots are detached before normalization.
			if sent.Downloaded != sentDownloaded {
				t.Fatalf("event State.Downloaded mutated: %d → %d", sentDownloaded, sent.Downloaded)
			}
			if sentHRBefore != nil {
				if sent.HLSResume != sentHRBefore {
					t.Fatal("event State.HLSResume pointer replaced in place")
				}
				if !reflect.DeepEqual(sentHRBefore, sentHRClone) {
					t.Fatal("event State.HLSResume contents mutated")
				}
			}
		})
	}
}
