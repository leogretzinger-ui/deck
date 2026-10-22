package session

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/noisefloor-oss/deck/internal/provider"
)

// codexLaunch is where one generation's launch keeps Codex auth metadata.
// An empty path means the launch named no home at all.
type codexLaunch struct {
	generation int
	path       string
}

// The exact env -i launch assignments are the source, not tmux's inherited
// environment. File metadata only suppresses optional first-login guidance;
// it establishes neither a valid credential nor a subscription.
func codexAuthMetadataPath(launchEnvironment []string, workDir string) (string, bool) {
	env := make(map[string]string)
	for _, assignment := range launchEnvironment {
		if key, value, ok := strings.Cut(assignment, "="); ok {
			env[key] = value
		}
	}
	if env["NOISEDECK_LOGIN_PROVIDER"] != "codex" || env["NOISEDECK_LOGIN_SOCKET"] == "" {
		return "", false
	}
	home := env["CODEX_HOME"]
	if home == "" {
		if env["HOME"] == "" {
			return "", true
		}
		home = filepath.Join(env["HOME"], ".codex")
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(workDir, home)
	}
	return filepath.Join(home, "auth.json"), true
}

func codexAuthMetadataMissing(path string) bool {
	if path == "" {
		return true
	}
	info, err := os.Stat(path)
	return err != nil || !info.Mode().IsRegular()
}

// recordCodexLaunch remembers the generation's auth metadata path and reports
// whether the launch offers login guidance. The record lives only in memory.
func (m *Manager) recordCodexLaunch(id string, generation int, launchEnvironment []string, workDir string) bool {
	path, ok := codexAuthMetadataPath(launchEnvironment, workDir)
	if !ok {
		m.codexLaunches.Delete(id)
		return false
	}
	m.codexLaunches.Store(id, codexLaunch{generation: generation, path: path})
	return codexAuthMetadataMissing(path)
}

func (m *Manager) codexLoginHelper(s *Session) bool {
	if s.Provider != string(provider.Codex) || s.Status != StatusRunning {
		return false
	}
	value, ok := m.codexLaunches.Load(s.ID)
	if !ok {
		return false
	}
	launch := value.(codexLaunch)
	return launch.generation == s.Generation && codexAuthMetadataMissing(launch.path)
}
