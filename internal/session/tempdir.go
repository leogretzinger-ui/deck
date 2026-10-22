package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

const (
	generationTempPrefix          = "g-"
	temporarySocketDigestSize     = 8
	temporaryGenerationDigestSize = 12
)

func hookDiagnosticsEnvironment(tempDir string) []string {
	value := "false"
	if tempDir != "" && os.Getenv("NOISEDECK_HOOK_DIAGNOSTICS") == "true" {
		value = "true"
	}
	return []string{"NOISEDECK_HOOK_DIAGNOSTICS=" + value}
}

// SetTemporaryRoot gives this manager a Deck-owned namespace beneath base. The
// tmux socket identity keeps independently tested or operated Deck namespaces
// from reclaiming one another's directories while remaining stable across a
// backend restart. This is a lifecycle partition, not isolation from another
// process running as the same user in the unconfined profile.
func (m *Manager) SetTemporaryRoot(base string) error {
	if !filepath.IsAbs(base) {
		return errors.New("session: temporary root must be absolute")
	}
	baseInfo, err := os.Lstat(base)
	if err != nil {
		return fmt.Errorf("session: inspect temporary base: %w", err)
	}
	if baseInfo.Mode()&os.ModeSymlink != 0 || !baseInfo.IsDir() {
		return errors.New("session: temporary base is not a real directory")
	}
	anchor := filepath.Join(base, "noisedeck-sessions")
	if err := os.Mkdir(anchor, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("session: create temporary anchor: %w", err)
	}
	if err := verifyPrivateDirectory(anchor); err != nil {
		return fmt.Errorf("session: verify temporary anchor: %w", err)
	}
	socketDigest := sha256.Sum256([]byte(m.tmux.SocketPath()))
	root := filepath.Join(anchor, hex.EncodeToString(socketDigest[:temporarySocketDigestSize]))
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("session: create temporary root: %w", err)
	}
	if err := verifyPrivateDirectory(root); err != nil {
		return fmt.Errorf("session: verify temporary root: %w", err)
	}
	m.tempRoot = root
	return nil
}

func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("path is not a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory mode is %04o, want owner-only", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("directory is not owned by the runtime user")
	}
	return nil
}

func generationTempName(sessionID string, generation int, marker string) string {
	digest := sha256.Sum256([]byte(sessionID + "\x00" + strconv.Itoa(generation) + "\x00" + marker))
	return generationTempPrefix + hex.EncodeToString(digest[:temporaryGenerationDigestSize])
}

func validGenerationTempName(name string) bool {
	if !strings.HasPrefix(name, generationTempPrefix) ||
		len(name) != len(generationTempPrefix)+temporaryGenerationDigestSize*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(name, generationTempPrefix))
	return err == nil
}

func (m *Manager) prepareGenerationTemp(sessionID string, generation int, marker string) (string, error) {
	if m.tempRoot == "" {
		return "", nil
	}
	if err := verifyPrivateDirectory(m.tempRoot); err != nil {
		return "", fmt.Errorf("session: temporary root changed: %w", err)
	}
	path := filepath.Join(m.tempRoot, generationTempName(sessionID, generation, marker))
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", fmt.Errorf("session: create generation temporary directory: %w", err)
	}
	if err := verifyPrivateDirectory(path); err != nil {
		_ = os.RemoveAll(path)
		return "", fmt.Errorf("session: verify generation temporary directory: %w", err)
	}
	return path, nil
}

func (m *Manager) removeGenerationTemp(sessionID string, generation int, marker string) error {
	if m.tempRoot == "" {
		return nil
	}
	if err := verifyPrivateDirectory(m.tempRoot); err != nil {
		return fmt.Errorf("session: temporary root changed: %w", err)
	}
	path := filepath.Join(m.tempRoot, generationTempName(sessionID, generation, marker))
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("session: remove generation temporary directory: %w", err)
	}
	return nil
}

// SweepTemporaryDirs reclaims only names in Deck's private namespace. A
// starting row, a provisional successor slot, a live exact process, or any
// member of its recorded process group preserves the matching generation.
func (m *Manager) SweepTemporaryDirs(ctx context.Context) error {
	if m.tempRoot == "" {
		return nil
	}
	if err := verifyPrivateDirectory(m.tempRoot); err != nil {
		return fmt.Errorf("session: temporary root changed: %w", err)
	}
	// Generation rows and successor slots are committed before their directory
	// is created. Listing candidates first and durable owners second therefore
	// prevents a concurrent create or recovery from being reclaimed by a stale
	// ownership snapshot.
	entries, err := os.ReadDir(m.tempRoot)
	if err != nil {
		return fmt.Errorf("session: read temporary root: %w", err)
	}
	owners, err := m.repo.ListTemporaryOwners(ctx)
	if err != nil {
		return err
	}
	keep := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		if owner.IdentityMarker == nil || *owner.IdentityMarker == "" {
			continue
		}
		preserve := owner.RemovedAt == nil && owner.Status == StatusStarting
		if owner.PanePID != nil && owner.StartTicks != nil {
			alive, observeErr := tmux.ProcAlive(*owner.PanePID, *owner.StartTicks)
			if observeErr != nil || alive {
				preserve = true
			}
		}
		if owner.ProcessGroupID != nil && *owner.ProcessGroupID > 1 {
			members, observeErr := tmux.GroupMembers(*owner.ProcessGroupID)
			if observeErr != nil || len(members) != 0 {
				preserve = true
			}
		}
		if preserve {
			keep[generationTempName(owner.ID, owner.Generation, *owner.IdentityMarker)] = struct{}{}
		}
	}
	slots, err := m.repo.ListSuccessorSlots(ctx)
	if err != nil {
		return err
	}
	for _, slot := range slots {
		keep[generationTempName(slot.SessionID, slot.CandidateGeneration,
			slot.CandidateIdentityMarker)] = struct{}{}
	}
	for _, entry := range entries {
		name := entry.Name()
		if !validGenerationTempName(name) {
			continue
		}
		if _, preserved := keep[name]; preserved {
			continue
		}
		if err := os.RemoveAll(filepath.Join(m.tempRoot, name)); err != nil {
			return fmt.Errorf("session: reclaim generation temporary directory: %w", err)
		}
	}
	return nil
}
