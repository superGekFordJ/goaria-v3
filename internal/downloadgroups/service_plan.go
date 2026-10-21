// Package downloadgroups provides folder planning, directory containment verification, and plan lifecycle management for grouped downloads.

package downloadgroups

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
	DownloadGroupKindCollection = "collection"
	DownloadGroupKindBatch      = "batch"
	DownloadGroupKindGeneric    = "download_group"

	DownloadGroupFolderMaxRunes = 100
)

type DownloadGroupPlan struct {
	mu        sync.Mutex
	group     rpc.DownloadGroup
	baseDir   string
	created   bool
	succeeded int
}

func NewDownloadGroupPlan(kind string, itemCount int, now time.Time) (*DownloadGroupPlan, error) {
	if itemCount <= 0 {
		return nil, errors.New("could not prepare download group folder")
	}
	if kind != DownloadGroupKindCollection && kind != DownloadGroupKindBatch && kind != DownloadGroupKindGeneric {
		kind = DownloadGroupKindGeneric
	}

	baseDir, err := ResolveDownloadGroupBaseDir(config.Get().DownloadDir)
	if err != nil {
		return nil, err
	}

	suffix := OpaqueDownloadGroupSuffix()
	timestamp := now.Format("2006-01-02 15-04-05")
	folderName, err := SafeDownloadGroupFolderName(kind, timestamp, "dg-"+suffix)
	if err != nil {
		return nil, err
	}
	dir, err := ResolveDownloadGroupDir(baseDir, folderName)
	if err != nil {
		return nil, err
	}

	label := DownloadGroupLabel(kind)
	return &DownloadGroupPlan{
		baseDir: baseDir,
		group: rpc.DownloadGroup{
			ID:         fmt.Sprintf("dg-%d-%s", now.Unix(), suffix),
			Kind:       kind,
			Name:       fmt.Sprintf("%s %s", label, timestamp),
			NameStatus: rpc.DownloadGroupNameStatusFallback,
			FolderName: folderName,
			Dir:        dir,
			ItemCount:  itemCount,
			CreatedAt:  now.Unix(),
		},
	}, nil
}

func NewDownloadGroupPlanWithFolderName(kind string, itemCount int, now time.Time, folderName string) (*DownloadGroupPlan, error) {
	sanitized := SanitizeDownloadGroupFolderName(folderName)
	if sanitized == "" {
		return NewDownloadGroupPlan(kind, itemCount, now)
	}
	if itemCount <= 0 {
		return nil, errors.New("could not prepare download group folder")
	}
	if kind != DownloadGroupKindCollection && kind != DownloadGroupKindBatch && kind != DownloadGroupKindGeneric {
		kind = DownloadGroupKindGeneric
	}

	baseDir, err := ResolveDownloadGroupBaseDir(config.Get().DownloadDir)
	if err != nil {
		return nil, err
	}
	dir, err := ResolveDownloadGroupDir(baseDir, sanitized)
	if err != nil {
		return nil, err
	}

	suffix := OpaqueDownloadGroupSuffix()
	return &DownloadGroupPlan{
		baseDir: baseDir,
		group: rpc.DownloadGroup{
			ID:         fmt.Sprintf("dg-%d-%s", now.Unix(), suffix),
			Kind:       kind,
			Name:       sanitized,
			NameStatus: rpc.DownloadGroupNameStatusStable,
			FolderName: sanitized,
			Dir:        dir,
			ItemCount:  itemCount,
			CreatedAt:  now.Unix(),
		},
	}, nil
}

func SafeDownloadGroupFolderName(kind, timestamp, suffix string) (string, error) {
	label := DownloadGroupLabel(kind)
	name := strings.TrimSpace(strings.Join([]string{label, timestamp, suffix}, " "))
	name = SanitizeDownloadGroupFolderName(name)
	if name == "" {
		return "", errors.New("could not prepare download group folder")
	}
	return name, nil
}

func ResolveDownloadGroupBaseDir(baseDir string) (string, error) {
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

func ResolveDownloadGroupDir(baseDir, folderName string) (string, error) {
	absBase, err := ResolveDownloadGroupBaseDir(baseDir)
	if err != nil {
		return "", err
	}
	folderName = SanitizeDownloadGroupFolderName(folderName)
	if folderName == "" || filepath.IsAbs(folderName) {
		return "", errors.New("could not prepare download group folder")
	}
	absGroup, err := filepath.Abs(filepath.Join(absBase, folderName))
	if err != nil {
		return "", errors.New("could not prepare download group folder")
	}
	if !DownloadGroupPathContained(absBase, absGroup) {
		return "", errors.New("could not prepare download group folder")
	}
	return absGroup, nil
}

func EnsureDownloadGroupDir(baseDir string, group *rpc.DownloadGroup) error {
	if group == nil {
		return nil
	}
	baseDir, err := ResolveDownloadGroupBaseDir(baseDir)
	if err != nil {
		return err
	}
	baseFolderName := SanitizeDownloadGroupFolderName(group.FolderName)
	if baseFolderName == "" {
		return errors.New("could not prepare download group folder")
	}

	for i := 1; i <= 99; i++ {
		folderName := baseFolderName
		if i > 1 {
			suffix := fmt.Sprintf("-%02d", i)
			baseRunes := []rune(baseFolderName)
			maxBaseRunes := DownloadGroupFolderMaxRunes - len([]rune(suffix))
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
		dir, err := ResolveDownloadGroupDir(baseDir, folderName)
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

func (p *DownloadGroupPlan) EnsureDir() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.created {
		return nil
	}
	if err := EnsureDownloadGroupDir(p.baseDir, &p.group); err != nil {
		return err
	}
	p.created = true
	return nil
}

func (p *DownloadGroupPlan) RecordSuccess() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.succeeded++
}

func (p *DownloadGroupPlan) CleanupIfUnused() {
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

func (p *DownloadGroupPlan) GroupCopy() rpc.DownloadGroup {
	if p == nil {
		return rpc.DownloadGroup{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.group
}

func CopyDownloadGroup(group *rpc.DownloadGroup) *rpc.DownloadGroup {
	if group == nil {
		return nil
	}
	copy := *group
	return &copy
}

func DownloadGroupLabel(kind string) string {
	switch kind {
	case DownloadGroupKindCollection:
		return "Collection"
	case DownloadGroupKindBatch:
		return "Batch"
	default:
		return "Download group"
	}
}

func SanitizeDownloadGroupFolderName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) || strings.ContainsRune("<>:\"/\\|?*", r) {
			continue
		}
		builder.WriteRune(r)
	}
	cleaned := strings.TrimRight(strings.TrimSpace(builder.String()), ". ")
	if cleaned == "" {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) > DownloadGroupFolderMaxRunes {
		cleaned = strings.TrimRight(string(runes[:DownloadGroupFolderMaxRunes]), ". ")
	}
	cleaned = strings.TrimSpace(cleaned)
	if isWindowsReservedFolderName(cleaned) {
		return ""
	}
	return cleaned
}

func isWindowsReservedFolderName(name string) bool {
	return IsWindowsReservedName(name)
}

func IsWindowsReservedName(name string) bool {
	upper := strings.ToUpper(name)
	if upper == "CONIN$" || upper == "CONOUT$" {
		return true
	}
	stem := reservedFolderStem(upper)
	stem = squeezeReservedFolderStem(stem)
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(stem) != 4 {
		return false
	}
	prefix := stem[:3]
	digit := stem[3]
	return (prefix == "COM" || prefix == "LPT") && digit >= '1' && digit <= '9'
}

func reservedFolderStem(upper string) string {
	for i, r := range upper {
		if isReservedFolderDot(r) {
			return upper[:i]
		}
	}
	return upper
}

func squeezeReservedFolderStem(stem string) string {
	var b strings.Builder
	for _, r := range stem {
		if unicode.IsSpace(r) || isReservedFolderDot(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isReservedFolderDot(r rune) bool {
	return r == '.' || r == '\uFF0E' || r == '\u3002' || r == ':'
}

func DownloadGroupPathContained(absBase, absGroup string) bool {
	rel, err := filepath.Rel(absBase, absGroup)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func OpaqueDownloadGroupSuffix() string {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(buf)
}
