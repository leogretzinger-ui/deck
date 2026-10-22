package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// starterCommitMessage is the only message this path ever writes.
const starterCommitMessage = "Practice project"

const maxStarterPaths = 200

var (
	// ErrStarterHistory reports a repository that already has commits other
	// than the starter commit this path would have made.
	ErrStarterHistory = errors.New("the repository already has history")
	// ErrStarterPaths reports a path list that is not a set of plain files
	// inside the project.
	ErrStarterPaths = errors.New("invalid starter file list")
	// ErrStarterIdentity reports a project with no repository-local author.
	ErrStarterIdentity = errors.New("the project has no Git author")
)

// CommitStarter makes the first commit of a freshly seeded project. It
// stages exactly the listed files and commits them under a fixed message
// with the author Deck already wrote into the repository. It refuses any
// repository that has history; a retry that finds exactly its own earlier
// commit reports created=false and changes nothing.
func CommitStarter(ctx context.Context, projectRoot string, allowedRoots, paths []string) (commit string, created bool, err error) {
	roots, err := canonicalRoots(allowedRoots)
	if err != nil {
		return "", false, err
	}
	// The stored path is revalidated against the roots in force now: a
	// project kept after the roots were narrowed is not committed to.
	canonical, err := Canonicalize(projectRoot)
	if err != nil {
		return "", false, err
	}
	if !underAnyRoot(canonical, roots) {
		return "", false, fmt.Errorf("%w: %s", ErrOutsideRoots, canonical)
	}
	projectRoot = canonical
	paths, err = cleanStarterPaths(projectRoot, paths)
	if err != nil {
		return "", false, err
	}
	if !gitDirConfined(projectRoot, roots) {
		return "", false, fmt.Errorf("%w: %s belongs to a repository outside the allowed roots", ErrOutsideRoots, projectRoot)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", false, fmt.Errorf("resolve Git executable: %w", err)
	}
	env := gitCeiling(roots)
	lockValue, _ := gitIdentityLocks.LoadOrStore(filepath.Clean(projectRoot), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	top, err := gitOutputIn(ctx, gitPath, projectRoot, env, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != filepath.Clean(projectRoot) {
		return "", false, fmt.Errorf("%w: the project is not the root of a Git repository", ErrNotGitRepository)
	}
	if head, headErr := gitOutputIn(ctx, gitPath, projectRoot, env, "rev-parse", "--verify", "--quiet", "HEAD"); headErr == nil {
		if sole, err := soleStarterCommit(ctx, gitPath, projectRoot, env, paths); err != nil {
			return "", false, err
		} else if sole {
			return head, false, nil
		}
		return "", false, ErrStarterHistory
	}
	for _, key := range []string{"user.name", "user.email"} {
		if _, ok, err := localGitValueAt(ctx, gitPath, projectRoot, key); err != nil {
			return "", false, err
		} else if !ok {
			return "", false, ErrStarterIdentity
		}
	}
	// Hooks and signing come from configuration outside the project, which
	// this fixed commit must not run or depend on. Paths are literal, so a
	// file named like pathspec magic stages as itself.
	hardened := []string{"--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}
	git := func(env []string, args ...string) (string, error) {
		return gitOutputIn(ctx, gitPath, projectRoot, env, append(append([]string{}, hardened...), args...)...)
	}
	// The commit is built in a private index, so entries the user has
	// already staged are neither committed nor disturbed.
	scratch, err := os.MkdirTemp("", "starter-index-")
	if err != nil {
		return "", false, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	isolated := append(append([]string{}, env...), "GIT_INDEX_FILE="+filepath.Join(scratch, "index"))
	if _, err := git(isolated, "read-tree", "--empty"); err != nil {
		return "", false, err
	}
	if _, err := git(isolated, append([]string{"add", "--"}, paths...)...); err != nil {
		return "", false, err
	}
	tree, err := git(isolated, "write-tree")
	if err != nil {
		return "", false, err
	}
	commit, err = git(env, "commit-tree", tree, "-m", starterCommitMessage)
	if err != nil {
		return "", false, err
	}
	// The all-zero old value makes the ref update fail if history appeared
	// since the check above.
	zero := strings.Repeat("0", len(commit))
	if _, err := git(env, "update-ref", "-m", starterCommitMessage, "HEAD", commit, zero); err != nil {
		return "", false, err
	}
	// Record the committed files in the working index too, so they do not
	// read as staged deletions; anything else staged stays as it was.
	if _, err := git(env, append([]string{"add", "--"}, paths...)...); err != nil {
		return "", false, err
	}
	return commit, true, nil
}

// soleStarterCommit reports whether HEAD is the one commit this path makes:
// a single commit with the fixed message whose tree is exactly paths.
func soleStarterCommit(ctx context.Context, gitPath, root string, env, paths []string) (bool, error) {
	count, err := gitOutputIn(ctx, gitPath, root, env, "rev-list", "--count", "HEAD")
	if err != nil || count != "1" {
		return false, err
	}
	subject, err := gitOutputIn(ctx, gitPath, root, env, "log", "-1", "--format=%s", "HEAD")
	if err != nil || subject != starterCommitMessage {
		return false, err
	}
	tree, err := gitOutputIn(ctx, gitPath, root, env, "-c", "core.quotePath=false", "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return false, err
	}
	committed := strings.Split(tree, "\n")
	sort.Strings(committed)
	return strings.Join(committed, "\n") == strings.Join(paths, "\n"), nil
}

// cleanStarterPaths returns the sorted, de-duplicated list after checking
// that each entry is a regular file reached through real directories only.
func cleanStarterPaths(root string, paths []string) ([]string, error) {
	if len(paths) == 0 || len(paths) > maxStarterPaths {
		return nil, fmt.Errorf("%w: expected 1 to %d files", ErrStarterPaths, maxStarterPaths)
	}
	directory, err := openProjectRoot(root, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if !fs.ValidPath(path) || path == "." || strings.ContainsAny(path, "\\\x00\n") {
			return nil, fmt.Errorf("%w: %q", ErrStarterPaths, path)
		}
		parts := strings.Split(path, "/")
		for _, part := range parts {
			if strings.EqualFold(part, ".git") {
				return nil, fmt.Errorf("%w: %q", ErrStarterPaths, path)
			}
		}
		if seen[path] {
			return nil, fmt.Errorf("%w: %q is listed twice", ErrStarterPaths, path)
		}
		seen[path] = true
		walk := "."
		for i, part := range parts {
			walk = filepath.Join(walk, part)
			info, err := directory.Lstat(walk)
			if err != nil {
				return nil, fmt.Errorf("%w: %q is missing", ErrStarterPaths, path)
			}
			last := i == len(parts)-1
			if (last && !info.Mode().IsRegular()) || (!last && !info.IsDir()) {
				return nil, fmt.Errorf("%w: %q is not a plain file", ErrStarterPaths, path)
			}
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

// StarterCommit commits the listed files of a freshly seeded project as its
// first commit. It exists so a guided exercise can start from a clean
// working tree without Deck accepting arbitrary Git commands.
func (h *Handler) StarterCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "unreadable starter commit request")
		return
	}
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
	commit, created, err := CommitStarter(r.Context(), p.CanonicalPath, h.allowedRoots, req.Paths)
	if err != nil {
		switch {
		case errors.Is(err, ErrStarterPaths):
			h.err(w, http.StatusBadRequest, "invalid_starter_files", err.Error())
		case errors.Is(err, ErrStarterHistory):
			h.err(w, http.StatusConflict, "history_not_empty", err.Error())
		case errors.Is(err, ErrStarterIdentity):
			h.err(w, http.StatusConflict, "git_identity_missing", err.Error())
		case errors.Is(err, ErrNotGitRepository):
			h.err(w, http.StatusConflict, "not_git_repository", err.Error())
		case errors.Is(err, ErrOutsideRoots):
			h.err(w, http.StatusForbidden, "outside_allowed_roots", err.Error())
		default:
			h.log.Error().Err(err).Msg("starter commit failed")
			h.err(w, http.StatusInternalServerError, "internal", "could not make the starter commit")
		}
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	h.json(w, status, map[string]any{"commit": commit, "created": created})
}
