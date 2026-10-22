package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestGitIdentityProfilesCASAndProjectSnapshots(t *testing.T) {
	repo, database := newTestRepo(t)
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	var nextID int
	repo.newID = func() (string, error) {
		nextID++
		return fmt.Sprintf("%032x", nextID), nil
	}
	ctx := context.Background()

	empty, err := repo.ListGitIdentityProfiles(ctx)
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if len(empty.Profiles) != 0 || empty.DefaultProfileID != nil || empty.Revision != 0 {
		t.Fatalf("empty catalog = %+v", empty)
	}

	personal, err := repo.CreateGitIdentityProfile(ctx, GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"})
	if err != nil {
		t.Fatalf("create personal: %v", err)
	}
	catalog, err := repo.ListGitIdentityProfiles(ctx)
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	if catalog.DefaultProfileID == nil || *catalog.DefaultProfileID != personal.ID || catalog.Revision != 1 {
		t.Fatalf("initial default = %+v", catalog)
	}
	if _, err := repo.CreateGitIdentityProfile(ctx, GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"}); !errors.Is(err, ErrGitIdentityDuplicate) {
		t.Fatalf("duplicate create = %v", err)
	}

	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectDir := mkdir(t, filepath.Join(root, "project"))
	project, _, err := repo.Register(ctx, "project", projectDir, []string{root}, GitIdentity{Name: personal.Name, Email: personal.Email})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	if project.GitIdentityProfileID == nil || *project.GitIdentityProfileID != personal.ID || project.GitIdentitySetupRevision != 1 {
		t.Fatalf("project snapshot = %+v", project)
	}

	if _, err := repo.UpdateGitIdentityProfile(ctx, personal.ID, 99, GitIdentity{Name: "New Author", Email: "new@example.invalid"}); !errors.Is(err, ErrGitIdentityRevision) {
		t.Fatalf("stale update = %v", err)
	}
	updated, err := repo.UpdateGitIdentityProfile(ctx, personal.ID, 1, GitIdentity{Name: "New Author", Email: "new@example.invalid"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Revision)
	}
	unchanged, err := repo.Get(ctx, project.ID)
	if err != nil {
		t.Fatalf("read project after profile edit: %v", err)
	}
	if unchanged.GitAuthorName != "Deck Author" || unchanged.GitAuthorEmail != "deck@example.invalid" || unchanged.GitIdentitySetupRevision != 1 {
		t.Fatalf("profile edit rewrote project snapshot: %+v", unchanged)
	}

	work, err := repo.CreateGitIdentityProfile(ctx, GitIdentity{Name: "Work Author", Email: "work@example.invalid"})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}
	if _, err := repo.SetDefaultGitIdentityProfile(ctx, work.ID, 0, work.Revision); !errors.Is(err, ErrGitIdentityRevision) {
		t.Fatalf("stale default = %v", err)
	}
	catalog, err = repo.SetDefaultGitIdentityProfile(ctx, work.ID, 1, work.Revision)
	if err != nil {
		t.Fatalf("set default: %v", err)
	}
	if catalog.DefaultProfileID == nil || *catalog.DefaultProfileID != work.ID || catalog.Revision != 2 {
		t.Fatalf("new default = %+v", catalog)
	}
	if err := repo.DeleteGitIdentityProfile(ctx, work.ID, work.Revision, catalog.Revision, "", 0); !errors.Is(err, ErrGitIdentityReplacement) {
		t.Fatalf("delete default without replacement = %v", err)
	}
	if err := repo.DeleteGitIdentityProfile(ctx, work.ID, work.Revision, catalog.Revision, personal.ID, updated.Revision); err != nil {
		t.Fatalf("delete default with replacement: %v", err)
	}
	catalog, err = repo.ListGitIdentityProfiles(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(catalog.Profiles) != 1 || catalog.DefaultProfileID == nil || *catalog.DefaultProfileID != personal.ID || catalog.Revision != 3 {
		t.Fatalf("catalog after delete = %+v", catalog)
	}

	var snapshots int
	if err := database.Reader().QueryRow(`
		SELECT count(*) FROM project_git_identity_snapshots WHERE project_id = ?`, project.ID,
	).Scan(&snapshots); err != nil {
		t.Fatalf("count project snapshots: %v", err)
	}
	if snapshots != 1 {
		t.Fatalf("project snapshots = %d, want 1", snapshots)
	}
}

func TestLegacyProjectIdentityEndpointUsesProfileSnapshotService(t *testing.T) {
	repo, database := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectDir := mkdir(t, filepath.Join(root, "project"))
	project, _, err := repo.Register(ctx, "project", projectDir, []string{root}, GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := repo.SetGitIdentity(ctx, project.ID, []string{root}, GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"}); err != nil {
		t.Fatalf("repeat legacy setup: %v", err)
	}
	var profiles, snapshots int
	if err := database.Reader().QueryRow(`SELECT count(*) FROM git_identity_profiles`).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := database.Reader().QueryRow(`SELECT count(*) FROM project_git_identity_snapshots WHERE project_id = ?`, project.ID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if profiles != 1 || snapshots != 1 {
		t.Fatalf("legacy setup profiles=%d snapshots=%d, want 1/1", profiles, snapshots)
	}
}

func TestSetGitIdentityReplacesRepositoryIdentityAndReportsIt(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectDir := mkdir(t, filepath.Join(root, "project"))
	project, _, err := repo.Register(ctx, "project", projectDir, []string{root},
		GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	testGit(t, projectDir, "config", "--local", "user.email", "by-hand@example.invalid")

	updated, replaced, err := repo.SetGitIdentity(ctx, project.ID, []string{root},
		GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"})
	if err != nil {
		t.Fatalf("set identity over a hand-edited repository: %v", err)
	}
	if updated.GitAuthorEmail != "deck@example.invalid" {
		t.Fatalf("project email = %q", updated.GitAuthorEmail)
	}
	if replaced == nil || replaced.Email != "by-hand@example.invalid" || replaced.Name != "" {
		t.Fatalf("replaced = %+v, want only the overwritten email", replaced)
	}
	if got := stringsTrim(testGit(t, projectDir, "config", "--local", "--get", "user.email")); got != "deck@example.invalid" {
		t.Fatalf("repository email = %q", got)
	}
}

func TestProfileRegistrationReplacesRepositoryIdentityAndReportsIt(t *testing.T) {
	repo, database := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	selected, err := repo.CreateGitIdentityProfile(ctx, GitIdentity{Name: "Selected Author", Email: "selected@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}

	dir := mkdir(t, filepath.Join(root, "existing"))
	testGit(t, dir, "init", "--quiet", "--initial-branch=main")
	testGit(t, dir, "config", "--local", "user.name", "Repository Author")
	testGit(t, dir, "config", "--local", "user.email", "repository@example.invalid")

	project, replaced, err := repo.RegisterWithGitIdentityProfile(ctx, "existing", dir, []string{root}, selected.ID, selected.Revision)
	if err != nil {
		t.Fatalf("registration over an existing repository identity: %v", err)
	}
	if project.GitIdentityProfileID == nil || *project.GitIdentityProfileID != selected.ID ||
		project.GitAuthorName != selected.Name || project.GitIdentitySetupRevision != 1 {
		t.Fatalf("registered project = %+v", project)
	}
	if replaced == nil || replaced.Name != "Repository Author" || replaced.Email != "repository@example.invalid" {
		t.Fatalf("replaced = %+v, want the previous repository-local values", replaced)
	}
	if got := stringsTrim(testGit(t, dir, "config", "--local", "--get", "user.email")); got != selected.Email {
		t.Fatalf("repository email = %q, want %q", got, selected.Email)
	}
	var projects int
	if err := database.Reader().QueryRow(`SELECT count(*) FROM projects WHERE canonical_path = ?`, dir).Scan(&projects); err != nil || projects != 1 {
		t.Fatalf("registered project count = %d err=%v", projects, err)
	}
}

func TestExistingProjectProfileSetupUsesProjectAndProfileCAS(t *testing.T) {
	repo, database := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectDir := mkdir(t, filepath.Join(root, "project"))
	project, _, err := repo.Register(ctx, "project", projectDir, []string{root}, GitIdentity{Name: "Original", Email: "original@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := repo.CreateGitIdentityProfile(ctx, GitIdentity{Name: "Selected", Email: "selected@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.SetGitIdentityProfile(ctx, project.ID, []string{root}, selected.ID, selected.Revision, 0); !errors.Is(err, ErrGitIdentityRevision) {
		t.Fatalf("stale project setup = %v", err)
	}
	updated, _, err := repo.SetGitIdentityProfile(ctx, project.ID, []string{root}, selected.ID, selected.Revision, project.GitIdentitySetupRevision)
	if err != nil {
		t.Fatalf("project setup: %v", err)
	}
	if updated.GitIdentitySetupRevision != 2 || updated.GitIdentityProfileID == nil || *updated.GitIdentityProfileID != selected.ID {
		t.Fatalf("updated project = %+v", updated)
	}
	idempotent, _, err := repo.SetGitIdentityProfile(ctx, project.ID, []string{root}, selected.ID, selected.Revision,
		updated.GitIdentitySetupRevision)
	if err != nil {
		t.Fatalf("repeat project setup: %v", err)
	}
	if idempotent.GitIdentitySetupRevision != updated.GitIdentitySetupRevision {
		t.Fatalf("repeat setup revision = %d, want %d", idempotent.GitIdentitySetupRevision, updated.GitIdentitySetupRevision)
	}
	var snapshots int
	if err := database.Reader().QueryRow(`SELECT count(*) FROM project_git_identity_snapshots WHERE project_id = ?`, project.ID).Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatalf("snapshots=%d err=%v", snapshots, err)
	}
}

func TestGitIdentityProfileHandlerRoundTrip(t *testing.T) {
	repo, _ := newTestRepo(t)
	handler := NewHandler(repo, nil, &fakeRemover{}, zerolog.Nop())
	mux := http.NewServeMux()
	handler.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}

	if recorder := do(http.MethodGet, "/api/git-identities", ""); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"profiles":[]`) {
		t.Fatalf("empty list = %d %s", recorder.Code, recorder.Body.String())
	}
	createBody, _ := json.Marshal(map[string]any{"name": "Deck Author", "email": "deck@example.invalid"})
	created := do(http.MethodPost, "/api/git-identities", string(createBody))
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var profile GitIdentityProfile
	if err := json.NewDecoder(created.Body).Decode(&profile); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	staleBody, _ := json.Marshal(map[string]any{
		"name": "Updated", "email": "updated@example.invalid", "expected_revision": 9,
	})
	stale := do(http.MethodPut, "/api/git-identities/"+profile.ID, string(staleBody))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"current_revision":1`) {
		t.Fatalf("stale update = %d %s", stale.Code, stale.Body.String())
	}
	defaultBody, _ := json.Marshal(map[string]any{
		"profile_id": profile.ID, "expected_revision": 1, "expected_profile_revision": 1,
	})
	if recorder := do(http.MethodPut, "/api/git-identities/default", string(defaultBody)); recorder.Code != http.StatusOK {
		t.Fatalf("idempotent default = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := do(http.MethodGet, "/api/git-identities/"+profile.ID, ""); recorder.Code != http.StatusOK {
		t.Fatalf("get = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestProjectRegistrationReportsReplacedRepositoryIdentity(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	profile, err := repo.CreateGitIdentityProfile(ctx, GitIdentity{
		Name: "Selected Author", Email: "selected@example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectDir := mkdir(t, filepath.Join(root, "project"))
	testGit(t, projectDir, "init", "--quiet", "--initial-branch=main")
	testGit(t, projectDir, "config", "--local", "user.name", "Repository Author")
	testGit(t, projectDir, "config", "--local", "user.email", "repository@example.invalid")

	handler := NewHandler(repo, []string{root}, &fakeRemover{}, zerolog.Nop())
	mux := http.NewServeMux()
	handler.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	body, _ := json.Marshal(map[string]any{
		"name":                           "project",
		"path":                           projectDir,
		"git_identity_profile_id":        profile.ID,
		"expected_git_identity_revision": profile.Revision,
	})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body))))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("registration status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		GitAuthorEmail string               `json:"git_author_email"`
		Replaced       *ReplacedGitIdentity `json:"replaced_git_identity"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if response.GitAuthorEmail != profile.Email {
		t.Fatalf("registered email = %q, want %q", response.GitAuthorEmail, profile.Email)
	}
	if response.Replaced == nil || response.Replaced.Name != "Repository Author" ||
		response.Replaced.Email != "repository@example.invalid" {
		t.Fatalf("replaced_git_identity = %+v, want the previous repository-local values", response.Replaced)
	}
}
