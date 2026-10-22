package project

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/noisefloor-oss/deck/internal/audit"
	"github.com/noisefloor-oss/deck/internal/db"
)

// Project is one registered directory.
type Project struct {
	ID                         string     `json:"id"`
	Name                       string     `json:"name"`
	CanonicalPath              string     `json:"canonical_path"`
	GitAuthorName              string     `json:"git_author_name"`
	GitAuthorEmail             string     `json:"git_author_email"`
	GitIdentityProfileID       *string    `json:"git_identity_profile_id,omitempty"`
	GitIdentityProfileRevision *int64     `json:"git_identity_profile_revision,omitempty"`
	GitIdentitySetupRevision   int64      `json:"git_identity_setup_revision"`
	CreatedAt                  time.Time  `json:"created_at"`
	LastUsedAt                 time.Time  `json:"last_used_at"`
	RemovedAt                  *time.Time `json:"removed_at,omitempty"`
}

// DirectoryEntry is one container directory the signed-in user may
// choose. Path is canonical and already confined to an allowed root.
type DirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// DirectoryListing is a server-side folder picker view. An empty CurrentPath
// is the virtual root that lists configured mounts; a real directory may have
// a parent only while that parent remains inside the allowlist.
type DirectoryListing struct {
	CurrentPath string           `json:"current_path"`
	ParentPath  *string          `json:"parent_path"`
	Directories []DirectoryEntry `json:"directories"`
}

// Audit event types. Registration and removal are auditable.
const (
	AuditCreated = string(audit.ProjectCreated)
	AuditRemoved = string(audit.ProjectRemoved)
	AuditRenamed = string(audit.ProjectRenamed)
)

var (
	// ErrNotFound reports an unknown or removed project id.
	ErrNotFound = errors.New("project not found")
	// ErrOutsideRoots reports a path that resolves outside every
	// allowlisted root.
	ErrOutsideRoots = errors.New("path outside allowed roots")
	// ErrInvalidDir reports a path that is not a usable directory: missing,
	// not a directory, or not writable by the runtime user.
	ErrInvalidDir = errors.New("not a valid writable directory")
	// ErrDuplicatePath reports an active project already registered for the
	// same canonical path.
	ErrDuplicatePath = errors.New("path already registered")
	// ErrRemovalBlocked reports a project that open task lanes still depend
	// on. The text is shown to the user as written.
	ErrRemovalBlocked = errors.New("project removal blocked")
	// ErrLiveSessions refuses an operation that requires no live session
	// under the project. The two operations mean different things by it: Git
	// identity setup refuses outright, and the user stops the sessions
	// first, while removal meets it only as the backstop behind the
	// handler's cascade (see Remove).
	ErrLiveSessions = errors.New("project has running sessions")
	// ErrGitIdentityRequired blocks session launch for projects imported before
	// setup recorded their repository-local attribution defaults.
	ErrGitIdentityRequired = errors.New("project Git identity setup is required")
)

// Repository owns the projects table.
type Repository struct {
	db    *db.DB
	now   func() time.Time
	newID func() (string, error)
}

// NewRepository wires the project registry.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d, now: time.Now, newID: newID}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("entropy unavailable: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Canonicalize resolves a registration path to its real absolute form:
// lexical absolute, then symlinks out, then absolute once more in case the
// link target was relative. The allowlist check runs against THIS
// result, never the user's spelling.
func Canonicalize(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks %q: %w: %w", path, err, ErrInvalidDir)
	}
	canonical, err := filepath.Abs(real)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	return canonical, nil
}

// UnderRoot reports whether canonical lies inside root — by path
// containment on the already-canonicalized inputs, so ".." tricks and
// symlink escapes were settled before this is called.
func UnderRoot(canonical, root string) bool {
	rel, err := filepath.Rel(root, canonical)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// BrowseDirectories returns only real directories visible inside the
// configured container roots. The browser never receives an unrestricted
// host-filesystem browser: each requested path and symlink target is resolved
// before the allowlist decision, using the same boundary as registration.
func BrowseDirectories(path string, allowedRoots []string) (*DirectoryListing, error) {
	roots, err := canonicalRoots(allowedRoots)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		entries := make([]DirectoryEntry, 0, len(roots))
		for _, root := range roots {
			entries = append(entries, DirectoryEntry{Name: filepath.Base(root), Path: root})
		}
		return &DirectoryListing{Directories: entries}, nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: project directory selection must be absolute", ErrInvalidDir)
	}
	current, err := Canonicalize(path)
	if err != nil {
		return nil, err
	}
	if !underAnyRoot(current, roots) {
		return nil, fmt.Errorf("%w: %s", ErrOutsideRoots, current)
	}
	openedRoots := make(map[string]*os.Root)
	defer func() {
		for _, root := range openedRoots {
			_ = root.Close()
		}
	}()
	openPath := func(path string) (*os.Root, string, error) {
		base, rel, err := allowedPathRoot(path, roots)
		if err != nil {
			return nil, "", err
		}
		root := openedRoots[base]
		if root == nil {
			root, err = openProjectRoot(base, nil)
			if err != nil {
				return nil, "", err
			}
			openedRoots[base] = root
		}
		return root, rel, nil
	}
	root, rel, err := openPath(current)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidDir, current)
	}
	info, err := root.Stat(rel)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidDir, current)
	}

	children, err := readDirInRoot(root, rel)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read %s: %w", ErrInvalidDir, current, err)
	}
	entries := make([]DirectoryEntry, 0, len(children))
	seen := make(map[string]struct{}, len(children))
	// Prefer real directory names over aliases when a symlink points at a
	// sibling. The second pass still exposes useful in-root symlinks when no
	// direct entry names their target.
	for _, symlinkPass := range []bool{false, true} {
		for _, child := range children {
			// Dot-directories are never project candidates: with
			// the root now a whole mixed workspace mount, entries like .git,
			// .cache, or provider homes would drown the picker. Hiding them
			// here is noise control, not an access boundary — an explicit
			// in-root dot path can still be browsed or registered directly.
			if strings.HasPrefix(child.Name(), ".") {
				continue
			}
			isSymlink := child.Type()&os.ModeSymlink != 0
			if isSymlink != symlinkPass {
				continue
			}
			candidate, err := Canonicalize(filepath.Join(current, child.Name()))
			if err != nil || !underAnyRoot(candidate, roots) {
				continue
			}
			candidateRoot, candidateRel, err := openPath(candidate)
			if err != nil {
				continue
			}
			candidateInfo, err := candidateRoot.Stat(candidateRel)
			if err != nil || !candidateInfo.IsDir() {
				continue
			}
			if _, duplicate := seen[candidate]; duplicate {
				continue
			}
			seen[candidate] = struct{}{}
			entries = append(entries, DirectoryEntry{Name: child.Name(), Path: candidate})
		}
	}

	listing := &DirectoryListing{CurrentPath: current, Directories: entries}
	parent := filepath.Dir(current)
	if parent != current && underAnyRoot(parent, roots) {
		listing.ParentPath = &parent
	}
	return listing, nil
}

// MakeDirectory creates one new folder inside an allowlisted directory
// (the picker lists only folders that exist, so a brand-new
// project needs a way to make one). The name must be a single plain
// path component; the parent is canonicalized and allowlist-checked
// exactly like a registration path, so this cannot become a write
// anywhere outside the configured roots.
func MakeDirectory(parent, name string, allowedRoots []string) (string, error) {
	return makeDirectory(parent, name, allowedRoots, fileAccessHooks{})
}

func makeDirectory(parent, name string, allowedRoots []string, hooks fileAccessHooks) (string, error) {
	if name == "" || name == "." || name == ".." ||
		name != strings.TrimSpace(name) ||
		filepath.Clean(name) != name ||
		strings.ContainsRune(name, '/') ||
		strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("%w: a new folder needs a single plain name", ErrInvalidDir)
	}
	if !filepath.IsAbs(parent) {
		return "", fmt.Errorf("%w: new-folder parent must be absolute", ErrInvalidDir)
	}
	roots, err := canonicalRoots(allowedRoots)
	if err != nil {
		return "", err
	}
	canonical, err := Canonicalize(parent)
	if err != nil {
		return "", err
	}
	if !underAnyRoot(canonical, roots) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoots, canonical)
	}
	info, err := allowedStat(canonical, roots)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrInvalidDir, canonical)
	}
	target := filepath.Join(canonical, name)
	directory, err := allowedDirectory(canonical, roots)
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()
	if hooks.parentOpened != nil {
		hooks.parentOpened()
	}
	if err := directory.Mkdir(name, 0o750); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("%w: %s", ErrDuplicatePath, target)
		}
		return "", fmt.Errorf("%w: cannot create %s: %w", ErrInvalidDir, target, err)
	}
	return target, nil
}

func canonicalRoots(roots []string) ([]string, error) {
	canonical := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		real, err := Canonicalize(root)
		if err != nil {
			return nil, fmt.Errorf("%w: configured project root %q is unavailable: %w", ErrInvalidDir, root, err)
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%w: configured project root %q is not a directory", ErrInvalidDir, root)
		}
		if _, duplicate := seen[real]; duplicate {
			continue
		}
		seen[real] = struct{}{}
		canonical = append(canonical, real)
	}
	return canonical, nil
}

func underAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if UnderRoot(path, root) {
			return true
		}
	}
	return false
}

// Register validates and records a project. The order is the security
// contract: canonicalize FIRST, then check the allowlist against the
// resolved path, then require a real writable directory. A path that passes
// none of these produces an explicit error, never a silent fallback.
func (r *Repository) Register(
	ctx context.Context,
	name, path string,
	allowedRoots []string,
	requested GitIdentity,
) (*Project, *ReplacedGitIdentity, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	// An empty path must not canonicalize: filepath.Abs("") resolves to
	// the process working directory, which could silently register the
	// backend's own directory when it sits under an allowlisted root.
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("path is required")
	}
	canonical, err := Canonicalize(path)
	if err != nil {
		return nil, nil, err
	}

	var roots []string
	for _, root := range allowedRoots {
		canonicalRoot, rootErr := Canonicalize(root)
		if rootErr == nil && UnderRoot(canonical, canonicalRoot) {
			roots = []string{canonicalRoot}
			break
		}
	}
	if len(roots) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrOutsideRoots, canonical)
	}

	info, err := allowedStat(canonical, roots)
	if err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("%w: %s", ErrInvalidDir, canonical)
	}
	if err := r.checkWritable(canonical, roots); err != nil {
		return nil, nil, err
	}
	// Reject a known duplicate before touching Git configuration. The partial
	// unique index below remains the concurrent authority.
	var existing string
	scanErr := r.db.Reader().QueryRowContext(ctx,
		`SELECT id FROM projects WHERE canonical_path = ? AND removed_at IS NULL`,
		canonical).Scan(&existing)
	if scanErr == nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrDuplicatePath, canonical)
	}
	identity, replaced, err := provisionGitIdentity(ctx, canonical, allowedRoots, requested)
	if err != nil {
		return nil, nil, err
	}

	p := &Project{
		Name:           strings.TrimSpace(name),
		CanonicalPath:  canonical,
		GitAuthorName:  identity.Name,
		GitAuthorEmail: identity.Email,
		CreatedAt:      r.now(),
		LastUsedAt:     r.now(),
	}
	if p.ID, err = r.newID(); err != nil {
		return nil, nil, err
	}
	// The friendly duplicate answer comes from a typed precheck; the
	// partial unique index remains the race-safe authority, with the
	// driver-message match as its (brittle, documented) translation.
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (id, name, canonical_path, git_author_name, git_author_email, created_at, last_used_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.CanonicalPath,
			p.GitAuthorName, p.GitAuthorEmail,
			db.FormatTime(p.CreatedAt), db.FormatTime(p.LastUsedAt)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: projects.canonical_path") {
				return fmt.Errorf("%w: %s", ErrDuplicatePath, canonical)
			}
			return fmt.Errorf("insert: %w", err)
		}
		if err := db.AppendAudit(ctx, tx, db.AuditEvent{
			Type:    AuditCreated,
			Details: fmt.Sprintf("id=%s path=%s", p.ID, p.CanonicalPath),
		}); err != nil {
			return err
		}
		return r.recordProjectGitIdentityTx(ctx, tx, p.ID, identity, p.CreatedAt)
	})
	if err != nil {
		return nil, nil, err
	}
	project, err := r.Get(ctx, p.ID)
	return project, replaced, err
}

// EnsureDirectory creates path when it does not exist, so a typed path that
// names nothing becomes a directory. Only the leaf is created, and only
// inside an allowed root under a parent that already exists; anything that
// already exists is left exactly as it is, including a file, which the
// registration then refuses as it always has.
func EnsureDirectory(path string, allowedRoots []string) error {
	return ensureDirectory(path, allowedRoots, fileAccessHooks{})
}

func ensureDirectory(path string, allowedRoots []string, hooks fileAccessHooks) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: project path must be absolute", ErrInvalidDir)
	}
	clean := filepath.Clean(path)
	roots, err := canonicalRoots(allowedRoots)
	if err != nil {
		return err
	}
	if canonical, resolveErr := Canonicalize(clean); resolveErr == nil {
		clean = canonical
	} else if parent, parentErr := Canonicalize(filepath.Dir(clean)); parentErr == nil {
		clean = filepath.Join(parent, filepath.Base(clean))
	}
	root, rel, err := openAllowedPath(clean, roots)
	if err != nil {
		return err
	}
	_, statErr := root.Lstat(rel)
	_ = root.Close()
	if statErr == nil {
		return nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("%w: %s: %w", ErrInvalidDir, clean, statErr)
	}
	parent, err := Canonicalize(filepath.Dir(clean))
	if err != nil {
		return fmt.Errorf("%w: the directory above %s does not exist", ErrInvalidDir, clean)
	}
	if !underAnyRoot(parent, roots) {
		return fmt.Errorf("%w: %s", ErrOutsideRoots, clean)
	}
	target := filepath.Join(parent, filepath.Base(clean))
	if !underAnyRoot(target, roots) {
		return fmt.Errorf("%w: %s", ErrOutsideRoots, target)
	}
	directory, err := allowedDirectory(parent, roots)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if hooks.parentOpened != nil {
		hooks.parentOpened()
	}
	if err := directory.Mkdir(filepath.Base(clean), 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: cannot create %s: %w", ErrInvalidDir, target, err)
	}
	return nil
}

// RegisterPlain records a directory as it is, without provisioning a Git
// identity: a directory that is not a repository has nothing to sign, and
// one inside a repository is governed by that repository's identity. The
// identity Git resolves at the path is recorded so the project reports what
// a commit there would carry; nothing is written to any configuration.
// Sessions stay gated until the project is given an identity of its own.
func (r *Repository) RegisterPlain(ctx context.Context, name, path string, allowedRoots []string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	canonical, err := Canonicalize(path)
	if err != nil {
		return nil, err
	}
	roots, err := canonicalRoots(allowedRoots)
	if err != nil {
		return nil, err
	}
	if !underAnyRoot(canonical, roots) {
		return nil, fmt.Errorf("%w: %s", ErrOutsideRoots, canonical)
	}
	info, err := allowedStat(canonical, roots)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidDir, canonical)
	}
	if err := r.checkWritable(canonical, roots); err != nil {
		return nil, err
	}
	var existing string
	if err := r.db.Reader().QueryRowContext(ctx,
		`SELECT id FROM projects WHERE canonical_path = ? AND removed_at IS NULL`,
		canonical).Scan(&existing); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrDuplicatePath, canonical)
	}
	p := &Project{Name: name, CanonicalPath: canonical, CreatedAt: r.now(), LastUsedAt: r.now()}
	if p.ID, err = r.newID(); err != nil {
		return nil, err
	}
	p.GitAuthorName, p.GitAuthorEmail = resolvedGitIdentity(ctx, canonical, roots)
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (id, name, canonical_path, git_author_name, git_author_email, created_at, last_used_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, p.ID, p.Name, p.CanonicalPath,
			p.GitAuthorName, p.GitAuthorEmail, db.FormatTime(p.CreatedAt), db.FormatTime(p.LastUsedAt)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: projects.canonical_path") {
				return fmt.Errorf("%w: %s", ErrDuplicatePath, canonical)
			}
			return fmt.Errorf("insert: %w", err)
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			Type: AuditCreated, Details: fmt.Sprintf("id=%s path=%s plain=true", p.ID, p.CanonicalPath),
		})
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, p.ID)
}

// resolvedGitIdentity is what Git itself would sign a commit at dir with:
// the enclosing repository's configuration when there is one, the
// user's otherwise, or nothing.
// Discovery is ceilinged at the roots, so a repository above a root never
// answers, and the .git entry git would follow is confined first, so an
// explicit pointer to an outside repository answers nothing either.
func resolvedGitIdentity(ctx context.Context, dir string, roots []string) (name, email string) {
	if !gitDirConfined(dir, roots) {
		return "", ""
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", ""
	}
	read := func(key string) string {
		cmd := exec.CommandContext(ctx, gitPath, "-C", dir, "config", "--get", key)
		cmd.Env = gitEnv(roots)
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return read("user.name"), read("user.email")
}

// RegisterWithGitIdentityProfile registers one directory from an exact active
// profile revision. Legacy name/email requests continue through Register for
// one compatibility release.
func (r *Repository) RegisterWithGitIdentityProfile(
	ctx context.Context,
	name, path string,
	allowedRoots []string,
	profileID string,
	expectedProfileRevision int64,
) (*Project, *ReplacedGitIdentity, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("path is required")
	}
	canonical, err := Canonicalize(path)
	if err != nil {
		return nil, nil, err
	}
	var roots []string
	for _, root := range allowedRoots {
		canonicalRoot, rootErr := Canonicalize(root)
		if rootErr == nil && UnderRoot(canonical, canonicalRoot) {
			roots = []string{canonicalRoot}
			break
		}
	}
	if len(roots) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrOutsideRoots, canonical)
	}

	info, err := allowedStat(canonical, roots)
	if err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("%w: %s", ErrInvalidDir, canonical)
	}
	if err := r.checkWritable(canonical, roots); err != nil {
		return nil, nil, err
	}
	var existing string
	if err := r.db.Reader().QueryRowContext(ctx,
		`SELECT id FROM projects WHERE canonical_path = ? AND removed_at IS NULL`,
		canonical).Scan(&existing); err == nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrDuplicatePath, canonical)
	}

	p := &Project{Name: name, CanonicalPath: canonical, CreatedAt: r.now(), LastUsedAt: r.now()}
	if p.ID, err = r.newID(); err != nil {
		return nil, nil, err
	}
	var replaced *ReplacedGitIdentity
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		selected, err := gitIdentityProfileAtRevisionTx(ctx, tx, profileID, expectedProfileRevision)
		if err != nil {
			return err
		}
		actual, actualReplaced, err := provisionGitIdentity(ctx, canonical, allowedRoots,
			GitIdentity{Name: selected.Name, Email: selected.Email})
		if err != nil {
			return err
		}
		replaced = actualReplaced
		p.GitAuthorName, p.GitAuthorEmail = actual.Name, actual.Email
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects (id, name, canonical_path, git_author_name, git_author_email, created_at, last_used_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, p.ID, p.Name, p.CanonicalPath,
			p.GitAuthorName, p.GitAuthorEmail, db.FormatTime(p.CreatedAt), db.FormatTime(p.LastUsedAt)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: projects.canonical_path") {
				return fmt.Errorf("%w: %s", ErrDuplicatePath, canonical)
			}
			return fmt.Errorf("insert: %w", err)
		}
		if err := db.AppendAudit(ctx, tx, db.AuditEvent{
			Type: AuditCreated, Details: fmt.Sprintf("id=%s path=%s", p.ID, p.CanonicalPath),
		}); err != nil {
			return err
		}
		return r.recordSelectedProjectGitIdentityTx(ctx, tx, p.ID, selected, 0, p.CreatedAt)
	})
	if err != nil {
		return nil, nil, err
	}
	project, err := r.Get(ctx, p.ID)
	return project, replaced, err
}

// SetGitIdentity completes or re-runs setup for a project registered before
// Deck wrote a git identity at registration. Repository-local values that
// differ are replaced and reported, never left in place silently.
func (r *Repository) SetGitIdentity(
	ctx context.Context,
	id string,
	allowedRoots []string,
	requested GitIdentity,
) (*Project, *ReplacedGitIdentity, error) {
	p, err := r.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var live int
	if err := r.db.Reader().QueryRowContext(ctx, `
		SELECT count(*) FROM sessions
		WHERE project_id = ? AND status IN ('starting', 'running')`, id).Scan(&live); err != nil {
		return nil, nil, fmt.Errorf("check project sessions before Git setup: %w", err)
	}
	if live > 0 {
		return nil, nil, fmt.Errorf("%w: stop active sessions before Git identity setup", ErrLiveSessions)
	}
	identity, replaced, err := provisionGitIdentity(ctx, p.CanonicalPath, allowedRoots, requested)
	if err != nil {
		return nil, nil, err
	}
	if err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return r.recordProjectGitIdentityTx(ctx, tx, id, identity, r.now())
	}); err != nil {
		return nil, nil, fmt.Errorf("record Git identity: %w", err)
	}
	project, err := r.Get(ctx, id)
	return project, replaced, err
}

// SetGitIdentityProfile applies one exact active profile revision to an
// existing project. It is also the in-context upgrade path for projects whose
// legacy row has no complete setup snapshot.
func (r *Repository) SetGitIdentityProfile(
	ctx context.Context,
	id string,
	allowedRoots []string,
	profileID string,
	expectedProfileRevision, expectedSetupRevision int64,
) (*Project, *ReplacedGitIdentity, error) {
	p, err := r.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var replaced *ReplacedGitIdentity
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var live int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sessions
			WHERE project_id = ? AND status IN ('starting', 'running')`, id).Scan(&live); err != nil {
			return fmt.Errorf("check project sessions before Git setup: %w", err)
		}
		if live > 0 {
			return fmt.Errorf("%w: stop active sessions before Git identity setup", ErrLiveSessions)
		}
		var currentSetupRevision int64
		if err := tx.QueryRowContext(ctx, `
			SELECT git_identity_setup_revision FROM projects
			WHERE id = ? AND removed_at IS NULL`, id).Scan(&currentSetupRevision); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("read project Git identity revision: %w", err)
		}
		if currentSetupRevision != expectedSetupRevision {
			return &GitIdentityRevisionConflict{Resource: "project", Current: currentSetupRevision}
		}
		selected, err := gitIdentityProfileAtRevisionTx(ctx, tx, profileID, expectedProfileRevision)
		if err != nil {
			return err
		}
		_, actualReplaced, err := provisionGitIdentity(ctx, p.CanonicalPath, allowedRoots,
			GitIdentity{Name: selected.Name, Email: selected.Email})
		if err != nil {
			return err
		}
		replaced = actualReplaced
		return r.recordSelectedProjectGitIdentityTx(ctx, tx, id, selected, expectedSetupRevision, r.now())
	})
	if err != nil {
		return nil, nil, err
	}
	project, err := r.Get(ctx, id)
	return project, replaced, err
}

// checkWritable uses a nonce so concurrent registrations collide on the unique index, not the probe.
// Remove it once, immediately: a second cleanup could delete a same-named file we do not own.
func (r *Repository) checkWritable(dir string, roots []string) error {
	directory, err := allowedDirectory(dir, roots)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	nonce, err := r.newID()
	if err != nil {
		return err
	}
	probe := ".noisedeck-write-probe-" + nonce
	f, err := directory.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidDir, dir, err)
	}
	_ = f.Close()
	if err := directory.Remove(probe); err != nil {
		return fmt.Errorf("remove write probe in %s: %w", dir, err)
	}
	return nil
}

// Get reads one active project.
func (r *Repository) Get(ctx context.Context, id string) (*Project, error) {
	row := r.db.Reader().QueryRowContext(ctx, `
		SELECT id, name, canonical_path, git_author_name, git_author_email,
		       git_identity_profile_id, git_identity_profile_revision, git_identity_setup_revision,
		       created_at, last_used_at, removed_at
		FROM projects WHERE id = ? AND removed_at IS NULL`, id)
	p, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// GetIncludingRemoved reads one project whether or not it was removed.
// Sessions outlive project removal, which keeps files and history, and
// native resume needs the working directory of a removed project's
// session — removal hides projects from the UI, it does not orphan their
// sessions' paths.
func (r *Repository) GetIncludingRemoved(ctx context.Context, id string) (*Project, error) {
	row := r.db.Reader().QueryRowContext(ctx, `
		SELECT id, name, canonical_path, git_author_name, git_author_email,
		       git_identity_profile_id, git_identity_profile_revision, git_identity_setup_revision,
		       created_at, last_used_at, removed_at
		FROM projects WHERE id = ?`, id)
	p, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// List returns every active project, most recently used first.
func (r *Repository) List(ctx context.Context) ([]*Project, error) {
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT id, name, canonical_path, git_author_name, git_author_email,
		       git_identity_profile_id, git_identity_profile_revision, git_identity_setup_revision,
		       created_at, last_used_at, removed_at
		FROM projects WHERE removed_at IS NULL ORDER BY last_used_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Project
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Remove hides a project. Its files are untouched — removal is a
// soft state precisely so the directory, the session history, and the
// audit trail all survive it.
//
// Removal is refused while any session under the project is recorded as
// starting or running. That refusal is the BACKSTOP, not what the user
// meets: the HTTP handler reads the live sessions once and asks the session
// manager to remove each, so a session reaching this check became
// live after that list was read — during the cascade or after it — or
// belongs to a caller that ran no cascade at all. The check reads the
// sessions table directly (importing internal/session here would be a cycle)
// and runs inside the SAME transaction as the update, so a session launched
// concurrently cannot slip between check and removal. The status set mirrors
// the session repository's ListActive definition of live.
func (r *Repository) Remove(ctx context.Context, id string) error {
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := removalBlocker(ctx, tx, id); err != nil {
			return err
		}
		live, err := liveSessionLabels(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(live) > 0 {
			return fmt.Errorf(
				"%w: %d session(s) still starting or running (%s) — stop them before removing the project",
				ErrLiveSessions, len(live), strings.Join(live, ", "))
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE projects SET removed_at = ? WHERE id = ? AND removed_at IS NULL`,
			db.FormatTime(r.now()), id)
		if err != nil {
			return fmt.Errorf("remove %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			Type:    AuditRemoved,
			Details: "id=" + id,
		})
	})
}

// SetName records the user's own display name for a project.
// The name is display metadata only, exactly like a session's
// display_name: the canonical path stays untouched and the
// directory on disk NEVER changes. There is no separate display-name
// column — projects.name has been pure display metadata since migration
// 001 (registration seeds it from the directory basename; nothing
// resolves paths through it), so the rename simply updates it. An empty
// name resets to the directory basename, the name registration would
// derive. The rename and its audit event commit in one transaction; a
// removed project has nothing left to name.
func (r *Repository) SetName(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if len(name) > 100 {
		return fmt.Errorf("project %s: display name over 100 characters", id)
	}
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if name == "" {
			var canonical string
			err := tx.QueryRowContext(ctx,
				`SELECT canonical_path FROM projects WHERE id = ? AND removed_at IS NULL`,
				id).Scan(&canonical)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("project %s: rename: %w", id, err)
			}
			name = filepath.Base(canonical)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE projects SET name = ? WHERE id = ? AND removed_at IS NULL`,
			name, id)
		if err != nil {
			return fmt.Errorf("project %s: rename: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			Type:    AuditRenamed,
			Details: fmt.Sprintf("id=%s name=%q", id, name),
		})
	})
}

// LiveSession identifies one still-live session for the removal
// cascade: the id the session manager needs, and the label the
// user sees if stopping it fails.
type LiveSession struct {
	ID    string
	Label string
}

// LiveSessions lists the project's sessions still recorded as starting
// or running — the rows removal must stop first. Same definition as
// the in-transaction backstop in Remove; this read is advisory (the
// cascade's input), the backstop stays authoritative.
func (r *Repository) LiveSessions(ctx context.Context, projectID string) ([]LiveSession, error) {
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT id, COALESCE(NULLIF(display_name, ''), provider)
		FROM sessions
		WHERE project_id = ? AND removed_at IS NULL
		  AND status IN ('starting', 'running')
		ORDER BY created_at`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list live sessions of %s: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []LiveSession
	for rows.Next() {
		var s LiveSession
		if err := rows.Scan(&s.ID, &s.Label); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// liveSessionLabels lists the user-facing names of the project's
// sessions still recorded as starting or running — the labels the sidebar
// shows (display_name, falling back to the provider), so the refusal
// names exactly the rows the user must stop.
func liveSessionLabels(ctx context.Context, tx *sql.Tx, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(display_name, ''), provider)
		FROM sessions
		WHERE project_id = ? AND removed_at IS NULL
		  AND status IN ('starting', 'running')
		ORDER BY created_at`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list live sessions of %s: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scan(s scanner) (*Project, error) {
	var p Project
	var created, lastUsed string
	var removed *string
	if err := s.Scan(&p.ID, &p.Name, &p.CanonicalPath, &p.GitAuthorName, &p.GitAuthorEmail,
		&p.GitIdentityProfileID, &p.GitIdentityProfileRevision, &p.GitIdentitySetupRevision,
		&created, &lastUsed, &removed); err != nil {
		return nil, err
	}
	var err error
	if p.CreatedAt, err = time.Parse(db.TimeFormat, created); err != nil {
		return nil, err
	}
	if p.LastUsedAt, err = time.Parse(db.TimeFormat, lastUsed); err != nil {
		return nil, err
	}
	if removed != nil {
		t, err := time.Parse(db.TimeFormat, *removed)
		if err != nil {
			return nil, err
		}
		p.RemovedAt = &t
	}
	return &p, nil
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// removalBlocker refuses the removal of a project that task lanes work on:
// a lane reaches its engine claim through its source project, so removing the
// source would strand it. Remove runs this inside its transaction, and lane
// creation checks the source inside its own, so the two cannot interleave.
func removalBlocker(ctx context.Context, q rowQuerier, id string) error {
	var lanes int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tutorial_lanes WHERE project_id = ?`, id).Scan(&lanes); err != nil {
		return fmt.Errorf("count lanes of %s: %w", id, err)
	}
	if lanes == 0 {
		return nil
	}
	noun, pronoun := "lane", "its task"
	if lanes > 1 {
		noun, pronoun = "lanes", "their tasks"
	}
	return fmt.Errorf("%w: this project has %d open task %s. Mark %s done first, or the lane's worktree and claim would be left with nothing able to close them", ErrRemovalBlocked, lanes, noun, pronoun)
}

// RemovalBlocker reports whether something stops the project's removal, so a
// caller can refuse before it stops anything. Remove repeats the check
// atomically.
func (r *Repository) RemovalBlocker(ctx context.Context, id string) error {
	return removalBlocker(ctx, r.db.Reader(), id)
}
