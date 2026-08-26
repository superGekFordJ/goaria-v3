package concurrent

import (
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestSequentialVsParallelChunking(t *testing.T) {
	// Setup RuntimeConfig
	minChunk := int64(2 * 1024 * 1024) // 2MB

	parallelConfig := &types.RuntimeConfig{
		SequentialDownload: false,
		MinChunkSize:       minChunk,
	}

	sequentialConfig := &types.RuntimeConfig{
		SequentialDownload: true,
		MinChunkSize:       minChunk,
	}

	totalSize := int64(100 * 1024 * 1024) // 100MB
	numConns := 4

	// Test Parallel: Should use large shards (FileSize / NumConns)
	dParallel := &ConcurrentDownloader{Runtime: parallelConfig}
	chunkSizeParallel := dParallel.determineChunkSize(totalSize, numConns)

	expectedParallel := totalSize / int64(numConns) // 25MB
	// It might be aligned, so check approx equality
	if chunkSizeParallel < expectedParallel-4096 || chunkSizeParallel > expectedParallel+4096 {
		t.Errorf("Parallel: expected approx %d, got %d", expectedParallel, chunkSizeParallel)
	}

	// Test Sequential: Should use MinChunkSize
	dSequential := &ConcurrentDownloader{Runtime: sequentialConfig}
	chunkSizeSeq := dSequential.determineChunkSize(totalSize, numConns)

	if chunkSizeSeq != minChunk {
		t.Errorf("Sequential: expected %d, got %d", minChunk, chunkSizeSeq)
	}
}

func TestTaskGenerationRequestOrder(t *testing.T) {
	// Verify that tasks are generated in increasing order
	fileSize := int64(10 * 1024 * 1024) // 10MB
	chunkSize := int64(2 * 1024 * 1024) // 2MB

	tasks := createTasks(fileSize, chunkSize)

	if len(tasks) != 5 {
		t.Errorf("Expected 5 tasks, got %d", len(tasks))
	}

	for i, task := range tasks {
		expectedOffset := int64(i) * chunkSize
		if task.Offset != expectedOffset {
			t.Errorf("Task %d: expected offset %d, got %d", i, expectedOffset, task.Offset)
		}
	}
}

func TestSetupTasks_SequentialDownloadUsesCreateTasks(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	fileSize := int64(100 * utils.MiB)
	numConns := 4
	destPath := filepath.Join(tmpDir, "sequential.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := &ConcurrentDownloader{
		ID:    "seq-setup",
		State: progress.New("seq-setup", fileSize),
		Runtime: &types.RuntimeConfig{
			SequentialDownload: true,
			MinChunkSize:       2 * utils.MiB,
		},
	}

	chunkSize := d.determineChunkSize(fileSize, numConns)
	tasks, err := d.setupTasks(destPath, fileSize, chunkSize, numConns, f, nil, false)
	if err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}
	if len(tasks) != 50 {
		t.Fatalf("sequential setupTasks produced %d tasks, want 50 (createTasks shards, not createInitialTasks)", len(tasks))
	}
}
