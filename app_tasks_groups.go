package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
)

const (
	downloadGroupKindCollection = "collection"
	downloadGroupKindBatch      = "batch"
	downloadGroupKindGeneric    = "download_group"

	downloadGroupFolderMaxRunes = 100
)

type downloadGroupPlan struct {
	mu        sync.Mutex
	group     rpc.DownloadGroup
	baseDir   string
	created   bool
	succeeded int
}

func newDownloadGroupPlan(kind string, itemCount int, now time.Time) (*downloadGroupPlan, error) {
	if itemCount <= 0 {
		return nil, errors.New("could not prepare download group folder")
	}
	if config.Current == nil {
		return nil, errors.New("could not prepare download group folder")
	}
	if kind != downloadGroupKindCollection && kind != downloadGroupKindBatch && kind != downloadGroupKindGeneric {
		kind = downloadGroupKindGeneric
	}

	baseDir, err := resolveDownloadGroupBaseDir(config.Current.DownloadDir)
	if err != nil {
		return nil, err
	}

	suffix := opaqueDownloadGroupSuffix()
	timestamp := now.Format("2006-01-02 15-04-05")
	folderName, err := safeDownloadGroupFolderName(kind, timestamp, "dg-"+suffix)
	if err != nil {
		return nil, err
	}
	dir, err := resolveDownloadGroupDir(baseDir, folderName)
	if err != nil {
		return nil, err
	}

	label := downloadGroupLabel(kind)
	return &downloadGroupPlan{
		baseDir: baseDir,
		group: rpc.DownloadGroup{
			ID:         fmt.Sprintf("dg-%d-%s", now.Unix(), suffix),
			Kind:       kind,
			Name:       fmt.Sprintf("%s %s", label, timestamp),
			FolderName: folderName,
			Dir:        dir,
			ItemCount:  itemCount,
			CreatedAt:  now.Unix(),
		},
	}, nil
}

func safeDownloadGroupFolderName(kind, timestamp, suffix string) (string, error) {
	label := downloadGroupLabel(kind)
	name := strings.TrimSpace(strings.Join([]string{label, timestamp, suffix}, " "))
	name = sanitizeDownloadGroupFolderName(name)
	if name == "" {
		return "", errors.New("could not prepare download group folder")
	}
	return name, nil
}

func resolveDownloadGroupBaseDir(baseDir string) (string, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return "", errors.New("could not prepare download group folder")
	}
	cleanBase := filepath.Clean(filepath.FromSlash(baseDir))
	absBase, err := filepath.Abs(cleanBase)
	if err != nil || strings.TrimSpace(absBase) == "" {
		return "", errors.New("could not prepare download group folder")
	}
	return absBase, nil
}

func resolveDownloadGroupDir(baseDir, folderName string) (string, error) {
	absBase, err := resolveDownloadGroupBaseDir(baseDir)
	if err != nil {
		return "", err
	}
	folderName = sanitizeDownloadGroupFolderName(folderName)
	if folderName == "" || filepath.IsAbs(folderName) {
		return "", errors.New("could not prepare download group folder")
	}
	absGroup, err := filepath.Abs(filepath.Join(absBase, folderName))
	if err != nil {
		return "", errors.New("could not prepare download group folder")
	}
	if !downloadGroupPathContained(absBase, absGroup) {
		return "", errors.New("could not prepare download group folder")
	}
	return absGroup, nil
}

func ensureDownloadGroupDir(baseDir string, group *rpc.DownloadGroup) error {
	if group == nil {
		return nil
	}
	baseDir, err := resolveDownloadGroupBaseDir(baseDir)
	if err != nil {
		return err
	}
	baseFolderName := sanitizeDownloadGroupFolderName(group.FolderName)
	if baseFolderName == "" {
		return errors.New("could not prepare download group folder")
	}

	for i := 1; i <= 99; i++ {
		folderName := baseFolderName
		if i > 1 {
			suffix := fmt.Sprintf("-%02d", i)
			baseRunes := []rune(baseFolderName)
			maxBaseRunes := downloadGroupFolderMaxRunes - len([]rune(suffix))
			if maxBaseRunes < 1 {
				return errors.New("could not prepare download group folder")
			}
			if len(baseRunes) > maxBaseRunes {
				folderName = strings.TrimRight(string(baseRunes[:maxBaseRunes]), ". ")
			} else {
				folderName = baseFolderName
			}
			folderName += suffix
		}
		dir, err := resolveDownloadGroupDir(baseDir, folderName)
		if err != nil {
			return err
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return errors.New("could not prepare download group folder")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errors.New("could not prepare download group folder")
		}
		group.FolderName = folderName
		group.Dir = dir
		return nil
	}

	return errors.New("could not prepare download group folder")
}

func (p *downloadGroupPlan) ensureDir() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.created {
		return nil
	}
	if err := ensureDownloadGroupDir(p.baseDir, &p.group); err != nil {
		return err
	}
	p.created = true
	return nil
}

func (p *downloadGroupPlan) recordSuccess() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.succeeded++
}

func (p *downloadGroupPlan) cleanupIfUnused() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.created || p.succeeded > 0 || p.group.Dir == "" {
		return
	}
	_ = os.Remove(p.group.Dir)
	p.created = false
}

func (p *downloadGroupPlan) groupCopy() rpc.DownloadGroup {
	if p == nil {
		return rpc.DownloadGroup{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.group
}

func copyDownloadGroup(group *rpc.DownloadGroup) *rpc.DownloadGroup {
	if group == nil {
		return nil
	}
	copy := *group
	return &copy
}

func downloadGroupLabel(kind string) string {
	switch kind {
	case downloadGroupKindCollection:
		return "Collection"
	case downloadGroupKindBatch:
		return "Batch"
	default:
		return "Download group"
	}
}

func sanitizeDownloadGroupFolderName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) || strings.ContainsRune(`<>:"/\\|?*`, r) {
			continue
		}
		builder.WriteRune(r)
	}
	cleaned := strings.TrimRight(strings.TrimSpace(builder.String()), ". ")
	if cleaned == "" {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) > downloadGroupFolderMaxRunes {
		cleaned = strings.TrimRight(string(runes[:downloadGroupFolderMaxRunes]), ". ")
	}
	return strings.TrimSpace(cleaned)
}

func downloadGroupPathContained(absBase, absGroup string) bool {
	rel, err := filepath.Rel(absBase, absGroup)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func opaqueDownloadGroupSuffix() string {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(buf)
}
