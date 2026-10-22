package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
)

// SessionRemover is the slice of the session manager project removal
// needs: stop and remove one session, with the manager's full identity
// checks and process teardown. Declared here because internal/session
// imports this package — the dependency arrow cannot point back.
type SessionRemover interface {
	Remove(ctx context.Context, id string) error
}

// Handler exposes the project registry over HTTP.
type Handler struct {
	repo         *Repository
	allowedRoots []string
	sessions     SessionRemover
	log          zerolog.Logger
	// clones runs repository clones as jobs; a clone is the one way of
	// adding a project that is not instant.
	clones           *Cloner
	onRemoved        func(context.Context, string)
	onRenamed        func(context.Context, string)
	githubRepository func(context.Context, string, int64) (string, error)
}

// OnRemoved releases project-scoped runtime access after removal succeeds.
func (h *Handler) OnRemoved(fn func(context.Context, string)) { h.onRemoved = fn }

// OnRenamed refreshes project-scoped runtime metadata after a rename
// commits: the name is display metadata, and consumers that cached it learn
// the new one from their next committed read.
func (h *Handler) OnRenamed(fn func(context.Context, string)) { h.onRenamed = fn }

// SetGitHubRepositoryResolver revalidates picker selections immediately before clone admission.
// It returns the current canonical HTTPS URL, or a safe user-facing refusal.
func (h *Handler) SetGitHubRepositoryResolver(resolve func(context.Context, string, int64) (string, error)) {
	h.githubRepository = resolve
}

// NewHandler wires the project endpoints. allowedRoots is the configured
// allowlist of roots a project may be registered under, applied to every
// registration. sessions runs the removal cascade's per-session teardown.
func NewHandler(repo *Repository, allowedRoots []string, sessions SessionRemover, log zerolog.Logger) *Handler {
	h := &Handler{repo: repo, allowedRoots: allowedRoots, sessions: sessions, log: log}
	h.clones = NewCloner(allowedRoots, h.registerClone, log)
	return h
}

// Mount registers the routes. handle and wrap are the server's registrar
// and the auth middleware, exactly as the terminal handler takes them.
func (h *Handler) Mount(handle func(pattern string, hd http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/git-identities", wrap(http.HandlerFunc(h.ListGitIdentities)))
	handle("POST /api/git-identities", wrap(http.HandlerFunc(h.CreateGitIdentity)))
	handle("PUT /api/git-identities/default", wrap(http.HandlerFunc(h.SetDefaultGitIdentity)))
	handle("GET /api/git-identities/{id}", wrap(http.HandlerFunc(h.GetGitIdentity)))
	handle("PUT /api/git-identities/{id}", wrap(http.HandlerFunc(h.UpdateGitIdentity)))
	handle("DELETE /api/git-identities/{id}", wrap(http.HandlerFunc(h.DeleteGitIdentity)))
	handle("POST /api/projects", wrap(http.HandlerFunc(h.Create)))
	handle("GET /api/projects/inspect", wrap(http.HandlerFunc(h.Inspect)))
	handle("POST /api/projects/clone", wrap(http.HandlerFunc(h.StartClone)))
	handle("GET /api/project-clones", wrap(http.HandlerFunc(h.ListClones)))
	handle("GET /api/project-clones/{id}", wrap(http.HandlerFunc(h.GetClone)))
	handle("POST /api/project-clones/{id}/cancel", wrap(http.HandlerFunc(h.CancelClone)))
	handle("POST /api/project-clones/{id}/resume", wrap(http.HandlerFunc(h.ResumeClone)))
	handle("DELETE /api/project-clones/{id}", wrap(http.HandlerFunc(h.DiscardClone)))
	handle("PUT /api/projects/{id}/git-identity", wrap(http.HandlerFunc(h.SetGitIdentity)))
	handle("GET /api/projects", wrap(http.HandlerFunc(h.List)))
	handle("GET /api/project-directories", wrap(http.HandlerFunc(h.BrowseDirectories)))
	handle("POST /api/project-directories/mkdir", wrap(http.HandlerFunc(h.MakeDirectory)))
	handle("GET /api/projects/{id}", wrap(http.HandlerFunc(h.Get)))
	handle("POST /api/projects/{id}/starter-commit", wrap(http.HandlerFunc(h.StarterCommit)))
	handle("POST /api/projects/{id}/rename", wrap(http.HandlerFunc(h.Rename)))
	handle("DELETE /api/projects/{id}", wrap(http.HandlerFunc(h.Remove)))
}

// ListGitIdentities returns every active reusable author profile and the
// revisioned default selection.
func (h *Handler) ListGitIdentities(w http.ResponseWriter, r *http.Request) {
	catalog, err := h.repo.ListGitIdentityProfiles(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("Git identity list failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not list Git identities")
		return
	}
	h.json(w, http.StatusOK, catalog)
}

// GetGitIdentity returns one active reusable author profile.
func (h *Handler) GetGitIdentity(w http.ResponseWriter, r *http.Request) {
	profile, err := h.repo.GetGitIdentityProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrGitIdentityProfileNotFound) {
			h.err(w, http.StatusNotFound, "not_found", "Git identity not found")
			return
		}
		h.log.Error().Err(err).Msg("Git identity read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read Git identity")
		return
	}
	h.json(w, http.StatusOK, profile)
}

// CreateGitIdentity creates one reusable non-secret author profile.
func (h *Handler) CreateGitIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable Git identity request")
		return
	}
	profile, err := h.repo.CreateGitIdentityProfile(r.Context(), GitIdentity{Name: req.Name, Email: req.Email})
	if err != nil {
		h.gitIdentityError(w, err)
		return
	}
	h.json(w, http.StatusCreated, profile)
}

// UpdateGitIdentity replaces one profile at an exact revision. Existing
// project snapshots are not rewritten.
func (h *Handler) UpdateGitIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string `json:"name"`
		Email            string `json:"email"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable Git identity request")
		return
	}
	if req.ExpectedRevision < 1 {
		h.err(w, http.StatusBadRequest, "bad_request", "expected_revision must be positive")
		return
	}
	profile, err := h.repo.UpdateGitIdentityProfile(r.Context(), r.PathValue("id"), req.ExpectedRevision,
		GitIdentity{Name: req.Name, Email: req.Email})
	if err != nil {
		h.gitIdentityError(w, err)
		return
	}
	h.json(w, http.StatusOK, profile)
}

// SetDefaultGitIdentity changes the default selection with both catalog and
// target-profile compare-and-swap revisions.
func (h *Handler) SetDefaultGitIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProfileID               string `json:"profile_id"`
		ExpectedRevision        int64  `json:"expected_revision"`
		ExpectedProfileRevision int64  `json:"expected_profile_revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable Git identity default request")
		return
	}
	if strings.TrimSpace(req.ProfileID) == "" || req.ExpectedRevision < 0 || req.ExpectedProfileRevision < 1 {
		h.err(w, http.StatusBadRequest, "bad_request", "profile and current revisions are required")
		return
	}
	catalog, err := h.repo.SetDefaultGitIdentityProfile(r.Context(), req.ProfileID, req.ExpectedRevision, req.ExpectedProfileRevision)
	if err != nil {
		h.gitIdentityError(w, err)
		return
	}
	h.json(w, http.StatusOK, catalog)
}

// DeleteGitIdentity soft-deletes one profile. If it is the current default,
// replacement_default_id selects the successor in the same transaction.
func (h *Handler) DeleteGitIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedRevision            int64  `json:"expected_revision"`
		CatalogRevision             int64  `json:"catalog_revision"`
		ReplacementDefaultID        string `json:"replacement_default_id"`
		ExpectedReplacementRevision int64  `json:"expected_replacement_revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable Git identity delete request")
		return
	}
	if req.ExpectedRevision < 1 || req.CatalogRevision < 0 {
		h.err(w, http.StatusBadRequest, "bad_request", "current revisions are required")
		return
	}
	if (req.ReplacementDefaultID == "") != (req.ExpectedReplacementRevision == 0) {
		h.err(w, http.StatusBadRequest, "bad_request", "replacement profile and revision must be provided together")
		return
	}
	if err := h.repo.DeleteGitIdentityProfile(r.Context(), r.PathValue("id"), req.ExpectedRevision,
		req.CatalogRevision, req.ReplacementDefaultID, req.ExpectedReplacementRevision); err != nil {
		h.gitIdentityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) gitIdentityError(w http.ResponseWriter, err error) {
	var conflict *GitIdentityRevisionConflict
	switch {
	case errors.As(err, &conflict):
		h.json(w, http.StatusConflict, map[string]any{
			"error":            "revision_conflict",
			"message":          err.Error(),
			"resource":         conflict.Resource,
			"current_revision": conflict.Current,
		})
	case errors.Is(err, ErrGitIdentityProfileNotFound):
		h.err(w, http.StatusNotFound, "not_found", "Git identity not found")
	case errors.Is(err, ErrGitIdentityDuplicate):
		h.err(w, http.StatusConflict, "git_identity_duplicate", err.Error())
	case errors.Is(err, ErrGitIdentityReplacement):
		h.err(w, http.StatusConflict, "replacement_default_required", err.Error())
	default:
		// Validation errors are deliberately user-facing. Database and other
		// internal failures retain their diagnostic only in the structured log.
		if errors.Is(err, ErrGitIdentityInvalid) || errors.Is(err, ErrGitIdentityRequest) {
			h.err(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		h.log.Error().Err(err).Msg("Git identity mutation failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not save Git identity")
	}
}

// BrowseDirectories serves the authenticated container-side folder picker.
// It exposes directories only within projects.allowed_roots, after resolving
// symlinks, so the browser cannot turn it into an unrestricted filesystem
// listing endpoint.
func (h *Handler) BrowseDirectories(w http.ResponseWriter, r *http.Request) {
	listing, err := BrowseDirectories(r.URL.Query().Get("path"), h.allowedRoots)
	if err != nil {
		switch {
		case errors.Is(err, ErrOutsideRoots):
			h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
		case errors.Is(err, ErrInvalidDir):
			h.err(w, http.StatusBadRequest, "invalid_directory", err.Error())
		default:
			h.err(w, http.StatusBadRequest, "bad_request", err.Error())
		}
		return
	}
	h.json(w, http.StatusOK, listing)
}

// MakeDirectory creates one new folder inside an allowlisted directory.
// The same boundary as registration applies: the parent is
// canonicalized and allowlist-checked server-side; the name must be a
// single plain path component.
func (h *Handler) MakeDirectory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable new-folder request")
		return
	}
	created, err := MakeDirectory(req.Path, req.Name, h.allowedRoots)
	if err != nil {
		switch {
		case errors.Is(err, ErrOutsideRoots):
			h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
		case errors.Is(err, ErrDuplicatePath):
			h.err(w, http.StatusConflict, "duplicate_path", err.Error())
		case errors.Is(err, ErrInvalidDir):
			h.err(w, http.StatusBadRequest, "invalid_directory", err.Error())
		default:
			h.err(w, http.StatusBadRequest, "bad_request", err.Error())
		}
		return
	}
	h.json(w, http.StatusCreated, map[string]string{"path": created})
}

// Inspect answers the add-project field: what the typed path or repository
// URL is, read from the filesystem inside the allowed roots and never from
// the network. base is the container a clone would land in.
func (h *Handler) Inspect(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := Inspect(r.Context(), q.Get("input"), q.Get("base"), h.allowedRoots, h.projectNameAt(r.Context()))
	if err != nil {
		switch {
		case errors.Is(err, ErrOutsideRoots):
			h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
		case errors.Is(err, ErrInvalidDir):
			h.err(w, http.StatusBadRequest, "invalid_directory", err.Error())
		default:
			h.log.Error().Err(err).Msg("project inspect failed")
			h.err(w, http.StatusInternalServerError, "internal", "could not inspect the path")
		}
		return
	}
	h.json(w, http.StatusOK, out)
}

// projectNameAt resolves a registered project's name by canonical path, so
// an enclosing repository is named the way the sidebar names it.
func (h *Handler) projectNameAt(ctx context.Context) func(path string) string {
	projects, err := h.repo.List(ctx)
	if err != nil {
		return func(string) string { return "" }
	}
	byPath := make(map[string]string, len(projects))
	for _, p := range projects {
		byPath[p.CanonicalPath] = p.Name
	}
	return func(path string) string { return byPath[path] }
}

// Create registers an allowlisted directory. A path that does not exist yet
// is created first — what is typed decides the shape, and an absent path
// means a new directory. A plain request registers the directory as it is:
// no Git provisioning, for a directory that is not a repository or sits
// inside one whose identity already governs it.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                        string `json:"name"`
		Path                        string `json:"path"`
		Plain                       bool   `json:"plain"`
		GitAuthorName               string `json:"git_author_name"`
		GitAuthorEmail              string `json:"git_author_email"`
		GitIdentityProfileID        string `json:"git_identity_profile_id"`
		ExpectedGitIdentityRevision int64  `json:"expected_git_identity_revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable project request")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		h.err(w, http.StatusBadRequest, "bad_request", "path is required")
		return
	}
	if err := EnsureDirectory(req.Path, h.allowedRoots); err != nil {
		switch {
		case errors.Is(err, ErrOutsideRoots):
			h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
		default:
			h.err(w, http.StatusBadRequest, "invalid_directory", err.Error())
		}
		return
	}
	var p *Project
	var replaced *ReplacedGitIdentity
	var err error
	if req.Plain {
		p, err = h.repo.RegisterPlain(r.Context(), req.Name, req.Path, h.allowedRoots)
	} else if strings.TrimSpace(req.GitIdentityProfileID) != "" {
		if req.ExpectedGitIdentityRevision < 1 {
			h.err(w, http.StatusBadRequest, "bad_request", "current Git identity revision is required")
			return
		}
		p, replaced, err = h.repo.RegisterWithGitIdentityProfile(r.Context(), req.Name, req.Path, h.allowedRoots,
			req.GitIdentityProfileID, req.ExpectedGitIdentityRevision)
	} else {
		p, replaced, err = h.repo.Register(r.Context(), req.Name, req.Path, h.allowedRoots,
			GitIdentity{Name: req.GitAuthorName, Email: req.GitAuthorEmail})
	}
	if err != nil {
		if h.writeGitIdentityRevisionError(w, err) {
			return
		}
		switch {
		case errors.Is(err, ErrOutsideRoots):
			h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
		case errors.Is(err, ErrDuplicatePath):
			h.err(w, http.StatusConflict, "duplicate_path", err.Error())
		case errors.Is(err, ErrNotGitRepository):
			h.err(w, http.StatusBadRequest, "not_git_repository", err.Error())
		case errors.Is(err, ErrInvalidDir):
			h.err(w, http.StatusBadRequest, "invalid_directory", err.Error())
		default:
			h.err(w, http.StatusBadRequest, "bad_request", err.Error())
		}
		return
	}
	h.addAssetsInstructions(p)
	h.json(w, http.StatusCreated, projectWriteResponse{Project: p, ReplacedGitIdentity: replaced})
}

// addAssetsInstructions tells the project's agents where files for the
// person go. Registration has already succeeded, so a failure is logged and
// the project stays registered.
func (h *Handler) addAssetsInstructions(p *Project) {
	if _, err := AddAssetsInstructions(p.CanonicalPath); err != nil {
		h.log.Warn().Err(err).Str("project", p.ID).Msg("assets instructions not written")
	}
}

// projectWriteResponse is a project plus, when the write overwrote
// repository-local Git settings, the values it replaced. The embedded
// project marshals inline, so a reader that only wants the project is
// unaffected; replaced_git_identity is absent when nothing was replaced.
type projectWriteResponse struct {
	*Project
	ReplacedGitIdentity *ReplacedGitIdentity `json:"replaced_git_identity,omitempty"`
}

// SetGitIdentity applies an author identity to a project registered before
// Deck wrote one at registration, or changes the one it has. It never accepts
// or stores credentials or signing material.
func (h *Handler) SetGitIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                         string `json:"name"`
		Email                        string `json:"email"`
		ProfileID                    string `json:"profile_id"`
		ExpectedProfileRevision      int64  `json:"expected_profile_revision"`
		ExpectedProjectSetupRevision int64  `json:"expected_project_setup_revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable Git identity request")
		return
	}
	var p *Project
	var replaced *ReplacedGitIdentity
	var err error
	if strings.TrimSpace(req.ProfileID) != "" {
		if req.ExpectedProfileRevision < 1 || req.ExpectedProjectSetupRevision < 0 {
			h.err(w, http.StatusBadRequest, "bad_request", "current Git identity and project revisions are required")
			return
		}
		p, replaced, err = h.repo.SetGitIdentityProfile(r.Context(), r.PathValue("id"), h.allowedRoots, req.ProfileID,
			req.ExpectedProfileRevision, req.ExpectedProjectSetupRevision)
	} else {
		p, replaced, err = h.repo.SetGitIdentity(r.Context(), r.PathValue("id"), h.allowedRoots, GitIdentity{Name: req.Name, Email: req.Email})
	}
	if err != nil {
		if h.writeGitIdentityRevisionError(w, err) {
			return
		}
		switch {
		case errors.Is(err, ErrNotFound):
			h.err(w, http.StatusNotFound, "not_found", "project not found")
		case errors.Is(err, ErrLiveSessions):
			h.err(w, http.StatusConflict, "live_sessions", err.Error())
		default:
			h.err(w, http.StatusBadRequest, "bad_request", err.Error())
		}
		return
	}
	h.json(w, http.StatusOK, projectWriteResponse{Project: p, ReplacedGitIdentity: replaced})
}

func (h *Handler) writeGitIdentityRevisionError(w http.ResponseWriter, err error) bool {
	var conflict *GitIdentityRevisionConflict
	if !errors.As(err, &conflict) {
		return false
	}
	h.gitIdentityError(w, err)
	return true
}

// List returns every active project.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	projects, err := h.repo.List(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal", "could not list projects")
		return
	}
	h.json(w, http.StatusOK, projects)
}

// Get returns one project.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			h.err(w, http.StatusNotFound, "not_found", "project not found")
			return
		}
		h.log.Error().Err(err).Msg("project read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read project")
		return
	}
	h.json(w, http.StatusOK, p)
}

// Rename records the user's own display name for a project, mirroring
// the session rename endpoint's shape and codes. The
// name is display metadata only — the canonical path stays as it was and
// the directory on disk never changes. An empty name resets to the
// directory basename, the name registration derives.
func (h *Handler) Rename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable rename request")
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 100 {
		h.err(w, http.StatusBadRequest, "bad_request", "display name over 100 characters")
		return
	}
	if err := h.repo.SetName(r.Context(), r.PathValue("id"), name); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			h.err(w, http.StatusNotFound, "not_found", "project not found")
		default:
			h.log.Error().Err(err).Msg("project rename failed")
			h.err(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	if h.onRenamed != nil {
		h.onRenamed(context.WithoutCancel(r.Context()), r.PathValue("id"))
	}
	w.WriteHeader(http.StatusNoContent)
}

// Remove hides a project. The directory's files are never touched.
//
// Live sessions do not refuse removal: the handler stops and removes each
// one through the session manager — the same
// teardown the sidebar's per-session control runs, in full view via
// the confirm dialog's stated consequence — then hides the project. A
// session the manager cannot stop (mid-launch, unverifiable process)
// fails the removal closed with a stable classification; the raw
// reason goes to the log, not the response. The repository's
// in-transaction live check remains the backstop for a session that
// launches between the cascade and the removal.
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.repo.RemovalBlocker(r.Context(), id); err != nil {
		if errors.Is(err, ErrRemovalBlocked) {
			h.err(w, http.StatusConflict, "removal_blocked", err.Error())
			return
		}
		h.log.Error().Err(err).Msg("project remove: blocker check failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not remove project")
		return
	}
	live, err := h.repo.LiveSessions(r.Context(), id)
	if err != nil {
		h.log.Error().Err(err).Msg("project remove: live-session listing failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not remove project")
		return
	}
	var stopped []string
	for _, s := range live {
		if err := h.sessions.Remove(r.Context(), s.ID); err != nil {
			h.log.Error().Err(err).Str("session", s.ID).
				Msg("project remove: session teardown failed")
			// The cascade is fail-fast, so sessions BEFORE this one are
			// already gone. Saying only "the project was not removed"
			// would read as a no-op; the user is told exactly what
			// this attempt destroyed on the way to failing.
			message := fmt.Sprintf(
				"session %q could not be stopped; the project was not removed", s.Label)
			if len(stopped) > 0 {
				message += fmt.Sprintf(" — %d session(s) were already stopped and removed (%s)",
					len(stopped), strings.Join(stopped, ", "))
			}
			h.err(w, http.StatusConflict, "session_stop_failed", message)
			return
		}
		stopped = append(stopped, s.Label)
	}
	if err := h.repo.Remove(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			h.err(w, http.StatusNotFound, "not_found", "project not found")
		case errors.Is(err, ErrLiveSessions):
			h.err(w, http.StatusConflict, "live_sessions", err.Error())
		default:
			h.log.Error().Err(err).Msg("project remove failed")
			h.err(w, http.StatusInternalServerError, "internal", "could not remove project")
		}
		return
	}
	if h.onRemoved != nil {
		h.onRemoved(context.WithoutCancel(r.Context()), id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// StopSessions stops and removes a project's running sessions and leaves the
// project registered.
func (h *Handler) StopSessions(ctx context.Context, id string) error {
	live, err := h.repo.LiveSessions(ctx, id)
	if err != nil {
		return err
	}
	for _, s := range live {
		if err := h.sessions.Remove(ctx, s.ID); err != nil {
			return fmt.Errorf("stop session %q: %w", s.Label, err)
		}
	}
	return nil
}

// RemoveProject unregisters a project for a caller that already decided to:
// its running sessions are stopped first, then the registration is removed.
func (h *Handler) RemoveProject(ctx context.Context, id string) error {
	if err := h.StopSessions(ctx, id); err != nil {
		return err
	}
	if err := h.repo.Remove(ctx, id); err != nil {
		return err
	}
	if h.onRemoved != nil {
		h.onRemoved(context.WithoutCancel(ctx), id)
	}
	return nil
}

func (h *Handler) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) err(w http.ResponseWriter, status int, code, message string) {
	h.json(w, status, map[string]string{"error": code, "message": message})
}

// Shutdown stops every running clone; what is on disk stays.
func (h *Handler) Shutdown() {
	h.clones.Shutdown()
}

// registerClone turns a finished clone into a project, with the identity
// the request named: a reusable profile at an exact revision, or an author.
func (h *Handler) registerClone(ctx context.Context, job CloneJob, identity CloneIdentity) (*Project, error) {
	var p *Project
	var err error
	if strings.TrimSpace(identity.ProfileID) != "" {
		p, _, err = h.repo.RegisterWithGitIdentityProfile(ctx, job.Name, job.Destination, h.allowedRoots,
			identity.ProfileID, identity.ExpectedRevision)
	} else {
		p, _, err = h.repo.Register(ctx, job.Name, job.Destination, h.allowedRoots,
			GitIdentity{Name: identity.Name, Email: identity.Email})
	}
	if err != nil {
		return nil, err
	}
	h.addAssetsInstructions(p)
	return p, nil
}

// StartClone begins cloning a remote repository into a directory under an
// allowed root and answers with the job. The URL must be remote: the
// boundary between a repository and a local path belongs here, at the API.
func (h *Handler) StartClone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL                         string `json:"url"`
		GitHubAccount               string `json:"github_account"`
		GitHubRepositoryID          int64  `json:"github_repository_id"`
		Destination                 string `json:"destination"`
		Name                        string `json:"name"`
		GitAuthorName               string `json:"git_author_name"`
		GitAuthorEmail              string `json:"git_author_email"`
		GitIdentityProfileID        string `json:"git_identity_profile_id"`
		ExpectedGitIdentityRevision int64  `json:"expected_git_identity_revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable clone request")
		return
	}
	if req.GitHubAccount != "" || req.GitHubRepositoryID != 0 {
		if h.githubRepository == nil || req.GitHubAccount == "" || req.GitHubRepositoryID <= 0 {
			h.err(w, http.StatusBadRequest, "github_selection_invalid", "Choose a GitHub repository again.")
			return
		}
		currentURL, err := h.githubRepository(r.Context(), req.GitHubAccount, req.GitHubRepositoryID)
		if err != nil {
			h.err(w, http.StatusConflict, "github_access_unconfirmed", err.Error())
			return
		}
		if req.URL != currentURL {
			h.err(w, http.StatusConflict, "github_repository_changed", "The repository moved or was renamed. Choose it again before importing.")
			return
		}
	}
	if err := ValidateCloneURL(req.URL); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.GitIdentityProfileID) != "" && req.ExpectedGitIdentityRevision < 1 {
		h.err(w, http.StatusBadRequest, "bad_request", "current Git identity revision is required")
		return
	}
	job, err := h.clones.Start(CloneRequest{
		URL: req.URL, Destination: req.Destination, Name: req.Name,
		GitHubAccount: req.GitHubAccount, GitHubRepositoryID: req.GitHubRepositoryID,
		Identity: CloneIdentity{
			ProfileID: req.GitIdentityProfileID, ExpectedRevision: req.ExpectedGitIdentityRevision,
			Name: req.GitAuthorName, Email: req.GitAuthorEmail,
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrOutsideRoots):
			h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
		case errors.Is(err, ErrCloneDestNotEmpty):
			h.err(w, http.StatusConflict, "destination_not_empty", err.Error())
		case errors.Is(err, ErrCloneInProgress):
			h.err(w, http.StatusConflict, "clone_in_progress", err.Error())
		case errors.Is(err, ErrCloneURLInvalid), errors.Is(err, ErrInvalidDir):
			h.err(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			h.log.Error().Err(err).Msg("clone start failed")
			h.err(w, http.StatusInternalServerError, "internal", "could not start the clone")
		}
		return
	}
	h.json(w, http.StatusAccepted, job)
}

func (h *Handler) ListClones(w http.ResponseWriter, _ *http.Request) {
	h.json(w, http.StatusOK, h.clones.List())
}

func (h *Handler) GetClone(w http.ResponseWriter, r *http.Request) {
	job, err := h.clones.Get(r.PathValue("id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "not_found", "clone job not found")
		return
	}
	h.json(w, http.StatusOK, job)
}

func (h *Handler) CancelClone(w http.ResponseWriter, r *http.Request) {
	job, err := h.clones.Cancel(r.PathValue("id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "not_found", "clone job not found")
		return
	}
	h.json(w, http.StatusOK, job)
}

func (h *Handler) ResumeClone(w http.ResponseWriter, r *http.Request) {
	previous, err := h.clones.Get(r.PathValue("id"))
	if err != nil {
		h.cloneStateError(w, err)
		return
	}
	if previous.GitHubRepositoryID != 0 {
		if h.githubRepository == nil {
			h.err(w, http.StatusConflict, "github_access_unconfirmed", "GitHub access could not be confirmed. The partial copy was kept.")
			return
		}
		currentURL, err := h.githubRepository(r.Context(), previous.GitHubAccount, previous.GitHubRepositoryID)
		if err != nil || currentURL != previous.URL {
			h.err(w, http.StatusConflict, "github_access_unconfirmed", "GitHub access or the repository address changed. The partial copy was kept. Refresh the repository list before importing again.")
			return
		}
	}
	job, err := h.clones.Resume(r.PathValue("id"))
	if err != nil {
		h.cloneStateError(w, err)
		return
	}
	h.json(w, http.StatusAccepted, job)
}

func (h *Handler) DiscardClone(w http.ResponseWriter, r *http.Request) {
	if err := h.clones.Discard(r.PathValue("id")); err != nil {
		h.cloneStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) cloneStateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCloneNotFound):
		h.err(w, http.StatusNotFound, "not_found", "clone job not found")
	case errors.Is(err, ErrCloneNotStopped):
		h.err(w, http.StatusConflict, "clone_running", err.Error())
	case errors.Is(err, ErrOutsideRoots):
		h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
	default:
		h.log.Error().Err(err).Msg("clone job update failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not update the clone")
	}
}
