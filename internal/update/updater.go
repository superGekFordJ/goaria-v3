package update

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/events"

	"github.com/minio/selfupdate"
)

// Updater manages the update download and apply process
type Updater struct {
	eventHub *events.Hub
	mu       sync.Mutex
	busy     bool
}

// NewUpdater creates a new Updater instance
func NewUpdater(hub *events.Hub) *Updater {
	return &Updater{
		eventHub: hub,
	}
}

// Apply downloads the update asset and applies it in-place.
// This method runs synchronously — callers should invoke it in a goroutine.
func (u *Updater) Apply(info *ReleaseInfo) error {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return errors.New("update already in progress")
	}
	u.busy = true
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	if info.AssetURL == "" {
		u.emitStatus(StatusError, "no asset URL provided")
		return errors.New("no asset URL provided")
	}

	// Emit downloading status
	u.emitStatus(StatusDownloading, nil)

	// Download the asset
	log.Printf("[Update] Downloading asset: %s", info.AssetURL)
	resp, err := http.Get(info.AssetURL)
	if err != nil {
		u.emitStatus(StatusError, err.Error())
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("download returned HTTP %d", resp.StatusCode)
		u.emitStatus(StatusError, msg)
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength
	if info.AssetSize > 0 {
		totalSize = info.AssetSize
	}

	// Emit initial progress with total size (for frontend smooth algorithm)
	u.emitProgress(0, totalSize, 0)

	// Read with progress tracking
	var buf bytes.Buffer
	reader := &progressReader{
		reader:    resp.Body,
		total:     totalSize,
		updater:   u,
		lastEmit:  time.Now(),
		emitEvery: 500 * time.Millisecond,
	}

	if _, err := io.Copy(&buf, reader); err != nil {
		u.emitStatus(StatusError, err.Error())
		return fmt.Errorf("download interrupted: %w", err)
	}

	u.emitProgress(int64(buf.Len()), totalSize, 0)
	log.Printf("[Update] Download complete (%d bytes)", buf.Len())

	// Extract .exe from zip
	exeReader, err := extractExeFromZip(buf.Bytes())
	if err != nil {
		u.emitStatus(StatusError, err.Error())
		return fmt.Errorf("zip extraction failed: %w", err)
	}

	// Apply the update using selfupdate
	log.Println("[Update] Applying update...")
	if err := selfupdate.Apply(exeReader, selfupdate.Options{}); err != nil {
		u.emitStatus(StatusError, err.Error())
		return fmt.Errorf("apply failed: %w", err)
	}

	log.Println("[Update] Update applied successfully")
	u.emitStatus(StatusReady, nil)
	return nil
}

// Restart launches a new instance of the application and exits the current one
func (u *Updater) Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable: %w", err)
	}

	log.Printf("[Update] Restarting application: %s", exe)
	cmd := exec.Command(exe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart failed: %w", err)
	}

	os.Exit(0)
	return nil // unreachable
}

// emitStatus sends an update status event to the frontend
func (u *Updater) emitStatus(status string, payload any) {
	if u.eventHub != nil {
		u.eventHub.EmitUpdateStatus(status, payload)
	}
}

// emitProgress sends an update progress event to the frontend
func (u *Updater) emitProgress(downloaded, total, speed int64) {
	if u.eventHub != nil {
		u.eventHub.EmitUpdateProgress(downloaded, total, speed)
	}
}

// progressReader wraps an io.Reader to track download progress
type progressReader struct {
	reader       io.Reader
	total        int64
	read         int64
	updater      *Updater
	lastEmit     time.Time
	lastReadMark int64
	emitEvery    time.Duration
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.read += int64(n)

	if pr.total > 0 && time.Since(pr.lastEmit) >= pr.emitEvery {
		elapsed := time.Since(pr.lastEmit).Seconds()
		var speed int64
		if elapsed > 0 {
			speed = int64(float64(pr.read-pr.lastReadMark) / elapsed)
		}
		pr.updater.emitProgress(pr.read, pr.total, speed)
		pr.lastReadMark = pr.read
		pr.lastEmit = time.Now()
	}

	return n, err
}

// extractExeFromZip finds and returns a reader for the .exe file inside a zip archive
func extractExeFromZip(data []byte) (io.Reader, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid zip: %w", err)
	}

	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if strings.HasSuffix(strings.ToLower(name), ".exe") && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("cannot open %s in zip: %w", f.Name, err)
			}
			// Read entire exe into memory so we can close the zip entry
			var buf bytes.Buffer
			if _, err := io.Copy(&buf, rc); err != nil {
				rc.Close()
				return nil, fmt.Errorf("cannot read %s from zip: %w", f.Name, err)
			}
			rc.Close()
			return &buf, nil
		}
	}

	return nil, errors.New("no .exe file found in zip archive")
}
