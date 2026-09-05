package store

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

func makeBenchmarkRecords(n int) []types.DownloadRecord {
	records := make([]types.DownloadRecord, n)
	for i := range n {
		records[i] = types.DownloadRecord{
			ID:              fmt.Sprintf("sg_%08d", i),
			URLHash:         fmt.Sprintf("hash%08d", i),
			URL:             fmt.Sprintf("https://example.com/file_%d.zip", i),
			Filename:        fmt.Sprintf("file_%d.zip", i),
			OutputPath:      fmt.Sprintf("D:/Downloads/file_%d.zip", i),
			DestPath:        fmt.Sprintf("D:/Downloads/file_%d.zip", i),
			TotalSize:       1024 * 1024 * 100,
			Downloaded:      1024 * 1024 * 50,
			Status:          "downloading",
			CreatedAt:       time.Now().Unix(),
			Elapsed:         15,
			AvgSpeed:        5 * 1024 * 1024,
			ActualChunkSize: 1024 * 1024,
			ChunkBitmap:     []byte{0xFF, 0xAA, 0x55, 0x00, 0xFF, 0x11, 0x22, 0x33},
			Workers:         8,
		}
	}
	return records
}

func BenchmarkGobMasterState_Encode_100(b *testing.B) {
	records := makeBenchmarkRecords(100)
	ms := MasterState{Version: 1, Downloads: records}

	b.ReportAllocs()
	for b.Loop() {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(&ms); err != nil {
			b.Fatalf("encode failed: %v", err)
		}
	}
}

func BenchmarkGobMasterState_Decode_100(b *testing.B) {
	records := makeBenchmarkRecords(100)
	ms := MasterState{Version: 1, Downloads: records}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&ms); err != nil {
		b.Fatalf("encode setup failed: %v", err)
	}
	encoded := buf.Bytes()

	b.ReportAllocs()
	for b.Loop() {
		var decoded MasterState
		r := bytes.NewReader(encoded)
		if err := gob.NewDecoder(r).Decode(&decoded); err != nil {
			b.Fatalf("decode failed: %v", err)
		}
	}
}

func BenchmarkGobMasterState_Encode_500(b *testing.B) {
	records := makeBenchmarkRecords(500)
	ms := MasterState{Version: 1, Downloads: records}

	b.ReportAllocs()
	for b.Loop() {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(&ms); err != nil {
			b.Fatalf("encode failed: %v", err)
		}
	}
}

func BenchmarkGobMasterState_Decode_500(b *testing.B) {
	records := makeBenchmarkRecords(500)
	ms := MasterState{Version: 1, Downloads: records}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&ms); err != nil {
		b.Fatalf("encode setup failed: %v", err)
	}
	encoded := buf.Bytes()

	b.ReportAllocs()
	for b.Loop() {
		var decoded MasterState
		r := bytes.NewReader(encoded)
		if err := gob.NewDecoder(r).Decode(&decoded); err != nil {
			b.Fatalf("decode failed: %v", err)
		}
	}
}
