package session

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/provider"
)

func TestDisplayNameRegistrationFollowsCommittedRenameAndClear(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	s, err := mgr.Create(context.Background(), CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	mgr.SetDisplayNameRegistrar(func(ctx context.Context, reg RosterRegistration) {
		stored, err := repo.Get(ctx, reg.SessionID)
		if err != nil || stored.DisplayName != reg.DisplayName || reg.SessionID != s.ID {
			t.Errorf("registration preceded its database commit: %v, %q", err, reg.DisplayName)
		}
		names = append(names, reg.DisplayName)
	})
	for _, body := range []string{`{"name":"Reviewer"}`, `{"name":""}`} {
		if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/rename", body); rec.Code != http.StatusNoContent {
			t.Fatalf("rename returned %d: %s", rec.Code, rec.Body.String())
		}
	}
	if !reflect.DeepEqual(names, []string{"Reviewer", ""}) {
		t.Fatalf("registered names = %q", names)
	}
	apiDo(t, mux, http.MethodPost, "/api/sessions/missing/rename", `{"name":"Wrong"}`)
	if len(names) != 2 {
		t.Fatal("failed rename registered a label")
	}
}

func TestDisplayNamesSurviveRecoveryAndStartupRestoration(t *testing.T) {
	desc := sleeperDescriptor()
	desc.contract = provider.IdentityFreshRelaunch
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	ctx := context.Background()
	labels := make(map[string]string)
	mgr.SetDisplayNameRegistrar(func(_ context.Context, reg RosterRegistration) { labels[reg.SessionID] = reg.DisplayName })
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(), DisplayName: "Initial name"})
	if err != nil {
		t.Fatal(err)
	}
	if labels[s.ID] != "Initial name" {
		t.Fatal("create did not register its name")
	}
	if err := mgr.Rename(ctx, s.ID, "Saved name"); err != nil {
		t.Fatal(err)
	}
	clear(labels)
	if err := mgr.RestoreDisplayNames(ctx); err != nil || labels[s.ID] != "Saved name" {
		t.Fatalf("startup restoration: %v, labels=%v", err, labels)
	}
	crashAndObserve(t, mgr, s)
	clear(labels)
	revived, _, err := mgr.Recover(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revived.ID != s.ID || labels[s.ID] != "Saved name" {
		t.Fatalf("recovery lost saved name: %v", labels)
	}
}

// projectNameResolver resolves committed project names exactly as boot wires
// it: through the projects repository over the session database.
func projectNameResolver(t *testing.T, repo *Repository) (*project.Repository, func(context.Context, string) (string, error)) {
	t.Helper()
	projects := project.NewRepository(repo.db)
	return projects, func(ctx context.Context, id string) (string, error) {
		p, err := projects.GetIncludingRemoved(ctx, id)
		if err != nil {
			return "", err
		}
		return p.Name, nil
	}
}

func TestRosterRegistrationCarriesCommittedProjectScope(t *testing.T) {
	desc := sleeperDescriptor()
	desc.contract = provider.IdentityFreshRelaunch
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	projects, resolve := projectNameResolver(t, repo)
	mgr.SetProjectNameResolver(resolve)
	ctx := context.Background()
	registrations := make(map[string]RosterRegistration)
	mgr.SetDisplayNameRegistrar(func(ctx context.Context, reg RosterRegistration) {
		// The registration must observe committed state, never a pending write.
		s, err := repo.Get(ctx, reg.SessionID)
		if err != nil || s.DisplayName != reg.DisplayName || s.ProjectID != reg.ProjectID {
			t.Errorf("registration preceded its commit: %v, %+v", err, reg)
		}
		if reg.ProjectID != "" {
			p, err := projects.Get(ctx, reg.ProjectID)
			if err != nil || p.Name != reg.ProjectName {
				t.Errorf("project scope preceded its commit: %v, %+v", err, reg)
			}
		}
		registrations[reg.SessionID] = reg
	})
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(), DisplayName: "Panel"})
	if err != nil {
		t.Fatal(err)
	}
	want := RosterRegistration{SessionID: s.ID, DisplayName: "Panel", ProjectID: projectID, ProjectName: "test"}
	if registrations[s.ID] != want {
		t.Fatalf("create registered %+v, want %+v", registrations[s.ID], want)
	}
	if err := mgr.Rename(ctx, s.ID, "Renamed panel"); err != nil {
		t.Fatal(err)
	}
	if got := registrations[s.ID]; got.ProjectID != projectID || got.ProjectName != "test" || got.DisplayName != "Renamed panel" {
		t.Fatalf("session rename dropped project scope: %+v", got)
	}
	crashAndObserve(t, mgr, s)
	if _, _, err := mgr.Recover(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if got := registrations[s.ID]; got.ProjectID != projectID || got.ProjectName != "test" {
		t.Fatalf("recovery registered %+v", got)
	}
	clear(registrations)
	if err := mgr.RestoreDisplayNames(ctx); err != nil {
		t.Fatal(err)
	}
	if got := registrations[s.ID]; got.ProjectID != projectID || got.ProjectName != "test" {
		t.Fatalf("startup restoration registered %+v", got)
	}
}

func TestProjectRenameRefreshesActiveSessionScope(t *testing.T) {
	mgr, repo, _, projectID := newAPIEnv(t)
	projects, resolve := projectNameResolver(t, repo)
	mgr.SetProjectNameResolver(resolve)
	ctx := context.Background()
	registrations := make(map[string]RosterRegistration)
	mgr.SetDisplayNameRegistrar(func(_ context.Context, reg RosterRegistration) { registrations[reg.SessionID] = reg })
	inScope, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	otherProject := insertRosterTestProject(t, repo, "prj-other", "other project")
	outOfScope, err := mgr.Create(ctx, CreateSpec{ProjectID: otherProject, Provider: provider.Claude, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.SetName(ctx, projectID, "renamed project"); err != nil {
		t.Fatal(err)
	}
	delete(registrations, inScope.ID)
	delete(registrations, outOfScope.ID)
	if err := mgr.SyncProjectRegistrations(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if got := registrations[inScope.ID]; got.ProjectID != projectID || got.ProjectName != "renamed project" {
		t.Fatalf("refresh registered %+v", got)
	}
	if _, refreshed := registrations[outOfScope.ID]; refreshed {
		t.Fatalf("refresh touched another project's session: %+v", registrations[outOfScope.ID])
	}
}

func TestProjectRenameHandlerCallbackDrivesRefresh(t *testing.T) {
	mgr, repo, _, projectID := newAPIEnv(t)
	projects, resolve := projectNameResolver(t, repo)
	mgr.SetProjectNameResolver(resolve)
	registrations := make(map[string]RosterRegistration)
	mgr.SetDisplayNameRegistrar(func(_ context.Context, reg RosterRegistration) { registrations[reg.SessionID] = reg })
	s, err := mgr.Create(context.Background(), CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	projectHandler := project.NewHandler(projects, nil, mgr, zerolog.Nop())
	// The wiring boot installs: a committed rename refreshes the project's
	// active sessions, and a refresh failure never fails the rename.
	projectHandler.OnRenamed(func(ctx context.Context, id string) {
		if err := mgr.SyncProjectRegistrations(ctx, id); err != nil {
			t.Errorf("refresh failed: %v", err)
		}
	})
	mux := http.NewServeMux()
	projectHandler.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	delete(registrations, s.ID)
	if rec := apiDo(t, mux, http.MethodPost, "/api/projects/"+projectID+"/rename", `{"name":"fresh name"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("project rename = %d: %s", rec.Code, rec.Body.String())
	}
	if got := registrations[s.ID]; got.ProjectID != projectID || got.ProjectName != "fresh name" {
		t.Fatalf("rename callback registered %+v", got)
	}
	delete(registrations, s.ID)
	if rec := apiDo(t, mux, http.MethodPost, "/api/projects/prj-missing/rename", `{"name":"nobody"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing project rename = %d", rec.Code)
	}
	if _, refreshed := registrations[s.ID]; refreshed {
		t.Fatal("a failed rename refreshed roster metadata")
	}
}

// insertRosterTestProject seeds a second project row under the same Git
// identity profile newTestEnv created, for cross-project refresh filtering.
func insertRosterTestProject(t *testing.T, repo *Repository, id, name string) string {
	t.Helper()
	err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		const profileID = "00000000000000000000000000000001"
		now := db.FormatTime(time.Now())
		_, err := tx.ExecContext(ctx, `
			INSERT INTO projects (
				id, name, canonical_path, git_author_name, git_author_email,
				git_identity_profile_id, git_identity_profile_revision, git_identity_setup_revision,
				created_at, last_used_at
			) VALUES (?, ?, ?, 'Test Author', 'test@example.invalid', ?, 1, 1, ?, ?)`,
			id, name, t.TempDir(), profileID, now, now)
		return err
	})
	if err != nil {
		t.Fatalf("insert project %s: %v", id, err)
	}
	return id
}

func TestRegistrationWithoutResolverKeepsLegacyArguments(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	s, err := mgr.Create(context.Background(), CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var registrations []RosterRegistration
	mgr.SetDisplayNameRegistrar(func(_ context.Context, reg RosterRegistration) { registrations = append(registrations, reg) })
	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/rename", `{"name":"Reviewer"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d", rec.Code)
	}
	if len(registrations) != 1 || registrations[0].ProjectID != "" || registrations[0].ProjectName != "" {
		t.Fatalf("resolver-less registration = %+v", registrations)
	}
	stored, err := repo.Get(context.Background(), s.ID)
	if err != nil || stored.ProjectID != projectID {
		t.Fatalf("session lost its project: %+v, %v", stored, err)
	}
}

func TestA2ADisplayNameRegistrarKeepsArgumentsSeparateAndFailurePrivate(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "args")
	helper := filepath.Join(dir, "a2a")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$LABEL_TEST_OUTPUT\"\n" +
		"test -z \"$A2A_HANDLE\" && test \"$A2A_NO_COURIER\" = 1 || exit 9\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABEL_TEST_OUTPUT", output)
	t.Setenv("A2A_HANDLE", "another-session")
	name := "-- Reviewer; $(touch never)"
	register := A2ADisplayNameRegistrar(helper, zerolog.Nop())
	register(context.Background(), RosterRegistration{SessionID: "session-id", DisplayName: name})
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "session-label\n--session\nsession-id\n--name="+name+"\n" {
		t.Fatalf("arguments = %q", raw)
	}
	// Committed project scope rides the same local registration as separate
	// arguments; a name that looks like a flag stays a value.
	projectName := "-- Project; $(touch never)"
	register(context.Background(), RosterRegistration{SessionID: "session-id", DisplayName: name, ProjectID: "prj-1", ProjectName: projectName})
	raw, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := "session-label\n--session\nsession-id\n--name=" + name + "\n--project-id=prj-1\n--project-name=" + projectName + "\n"
	if string(raw) != want {
		t.Fatalf("arguments = %q, want %q", raw, want)
	}
	var logs bytes.Buffer
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho private-child-output >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	A2ADisplayNameRegistrar(helper, zerolog.New(&logs))(context.Background(), RosterRegistration{SessionID: "session-id", DisplayName: name, ProjectID: "prj-1", ProjectName: projectName})
	for _, secret := range []string{"private-child-output", name, projectName, "prj-1"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("registration failure log leaked %q: %s", secret, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "could not be registered") {
		t.Fatalf("registration failure log = %s", logs.String())
	}
}

func TestUnavailableA2AHelperDoesNotUndoRename(t *testing.T) {
	mgr, repo, mux, projectID := newAPIEnv(t)
	s, err := mgr.Create(context.Background(), CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetDisplayNameRegistrar(A2ADisplayNameRegistrar(filepath.Join(t.TempDir(), "missing"), zerolog.Nop()))
	if rec := apiDo(t, mux, http.MethodPost, "/api/sessions/"+s.ID+"/rename", `{"name":"Saved"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d", rec.Code)
	}
	stored, err := repo.Get(context.Background(), s.ID)
	if err != nil || stored.DisplayName != "Saved" {
		t.Fatalf("saved name after unavailable helper: %v, %v", stored, err)
	}
}

func TestA2ADisplayNameRegistrationIsBounded(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "a2a")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	A2ADisplayNameRegistrar(helper, zerolog.Nop())(context.Background(), RosterRegistration{SessionID: "session-id", DisplayName: "Name"})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("local registration stalled for %s", elapsed)
	}
}
