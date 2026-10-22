package session

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// RosterRegistration carries committed session and project display metadata.
// Empty project fields leave previously registered project metadata intact.
type RosterRegistration struct {
	SessionID   string
	DisplayName string
	ProjectID   string
	ProjectName string
}

// SetDisplayNameRegistrar installs the local roster metadata writer at boot.
func (m *Manager) SetDisplayNameRegistrar(register func(context.Context, RosterRegistration)) {
	m.registerDisplayName = register
}

// SetProjectNameResolver supplies project names from Deck's repository.
func (m *Manager) SetProjectNameResolver(resolve func(context.Context, string) (string, error)) {
	m.projectName = resolve
}

// Rename commits the user's label before updating the optional roster.
func (m *Manager) Rename(ctx context.Context, id, name string) error {
	m.displayNameMu.Lock()
	defer m.displayNameMu.Unlock()
	if err := m.repo.SetDisplayName(ctx, id, name); err != nil {
		return err
	}
	m.registerRoster(ctx, id)
	return nil
}

// registerRoster reads committed metadata while the caller holds displayNameMu.
func (m *Manager) registerRoster(ctx context.Context, id string) {
	if m.registerDisplayName == nil {
		return
	}
	// A rename may have committed since launch or recovery read its source.
	s, err := m.repo.Get(ctx, id)
	if err != nil {
		return
	}
	registration := RosterRegistration{SessionID: id, DisplayName: s.DisplayName}
	if m.projectName != nil && s.ProjectID != "" {
		if name, err := m.projectName(ctx, s.ProjectID); err == nil && name != "" {
			registration.ProjectID = s.ProjectID
			registration.ProjectName = name
		}
	}
	m.registerDisplayName(ctx, registration)
}

func (m *Manager) syncDisplayName(ctx context.Context, id string) {
	if m.registerDisplayName == nil {
		return
	}
	m.displayNameMu.Lock()
	defer m.displayNameMu.Unlock()
	m.registerRoster(ctx, id)
}

// SyncProjectRegistrations refreshes active sessions after a project rename.
func (m *Manager) SyncProjectRegistrations(ctx context.Context, projectID string) error {
	if m.registerDisplayName == nil {
		return nil
	}
	sessions, err := m.repo.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.ProjectID == projectID {
			m.syncDisplayName(ctx, s.ID)
		}
	}
	return nil
}

// RestoreDisplayNames reconstructs metadata for sessions surviving a restart.
// The roster still discovers live agents from processes, never from these rows.
func (m *Manager) RestoreDisplayNames(ctx context.Context) error {
	if m.registerDisplayName == nil {
		return nil
	}
	sessions, err := m.repo.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		m.syncDisplayName(ctx, s.ID)
	}
	return nil
}

// A2ADisplayNameRegistrar uses a2a's local-only registration interface. An older
// or unavailable helper cannot undo a saved name or a successful launch.
func A2ADisplayNameRegistrar(executable string, log zerolog.Logger) func(context.Context, RosterRegistration) {
	return func(parent context.Context, registration RosterRegistration) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		defer cancel()
		args := []string{"session-label", "--session", registration.SessionID, "--name=" + registration.DisplayName}
		if registration.ProjectID != "" && registration.ProjectName != "" {
			args = append(args, "--project-id="+registration.ProjectID, "--project-name="+registration.ProjectName)
		}
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.WaitDelay = time.Second
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "A2A_HANDLE=") && !strings.HasPrefix(entry, "A2A_NO_COURIER=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "A2A_NO_COURIER=1")
		if err := cmd.Run(); err != nil {
			log.Warn().Str("session", registration.SessionID).Msg("saved session metadata could not be registered with a2a")
		}
	}
}
