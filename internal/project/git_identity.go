package project

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GitIdentity is the project-scoped, non-secret commit attribution default.
type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ReplacedGitIdentity names the repository-local values a setup write
// overwrote, so the user can see what changed and put it back with one
// `git config --local` in their own repository. It carries only
// repository-local facts: Deck never consults or exposes user-global Git
// configuration. A field is empty when that key was absent or already
// matched, so a nil ReplacedGitIdentity means the write replaced nothing.
type ReplacedGitIdentity struct {
	Name          string `json:"name,omitempty"`
	Email         string `json:"email,omitempty"`
	UseConfigOnly string `json:"use_config_only,omitempty"`
}

var (
	ErrGitIdentityInvalid = errors.New("invalid Git identity")
	ErrNotGitRepository   = errors.New("project is not a Git repository root")
	gitIdentityLocks      sync.Map
)

func validateGitIdentity(identity GitIdentity) (GitIdentity, error) {
	identity.Name = strings.TrimSpace(identity.Name)
	identity.Email = strings.TrimSpace(identity.Email)
	if identity.Name == "" || strings.ContainsAny(identity.Name, "\r\n\x00") {
		return GitIdentity{}, fmt.Errorf("%w: Git author name is required and must be one line", ErrGitIdentityInvalid)
	}
	address, err := mail.ParseAddress(identity.Email)
	if err != nil || address.Name != "" || address.Address != identity.Email || strings.ContainsAny(identity.Email, "\r\n\x00") {
		return GitIdentity{}, fmt.Errorf("%w: Git author email must be a plain valid address", ErrGitIdentityInvalid)
	}
	return identity, nil
}

func gitOutput(ctx context.Context, gitPath, dir string, args ...string) (string, error) {
	return gitOutputIn(ctx, gitPath, dir, gitEnv(nil), args...)
}

// gitEnv is the environment every git this package runs receives: the
// process's own, with any variable that selects a repository removed —
// an inherited GIT_DIR or GIT_WORK_TREE would otherwise point git at a
// repository the path confinement never saw — and, when roots are given,
// discovery ceilinged at their parents so a repository above a root is
// never found.
func gitEnv(roots []string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if gitRepositorySelector(key) {
			continue
		}
		env = append(env, kv)
	}
	if len(roots) == 0 {
		return env
	}
	ceilings := make([]string, 0, len(roots))
	for _, root := range roots {
		ceilings = append(ceilings, filepath.Dir(root))
	}
	return append(env, "GIT_CEILING_DIRECTORIES="+strings.Join(ceilings, ":"))
}

// gitRepositorySelector names the environment variables through which git
// chooses or relocates a repository, index or object store.
func gitRepositorySelector(key string) bool {
	switch key {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
		"GIT_NAMESPACE", "GIT_IMPLICIT_WORK_TREE", "GIT_PREFIX":
		return true
	}
	return false
}

func gitCeiling(roots []string) []string {
	return gitEnv(roots)
}

func gitOutputIn(ctx context.Context, gitPath, dir string, env []string, args ...string) (string, error) {
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, gitPath, argv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func localGitValue(ctx context.Context, dir, key string) (string, bool, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", false, fmt.Errorf("resolve Git executable: %w", err)
	}
	return localGitValueAt(ctx, gitPath, dir, key)
}

// localGitValuesAt reads every local value for a key, not just the last one.
// A git key may hold several, and the write below uses --replace-all, which
// destroys all of them - so a reader that saw only one would name one value
// while the write removed several, and the report of what was replaced is the
// whole reason the write is allowed to proceed unasked.
func localGitValuesAt(ctx context.Context, gitPath, dir, key string) ([]string, error) {
	return localGitValuesIn(ctx, gitPath, dir, gitEnv(nil), key)
}

func localGitValuesIn(ctx context.Context, gitPath, dir string, env []string, key string) ([]string, error) {
	cmd := exec.CommandContext(ctx, gitPath, "-C", dir, "config", "--local", "--get-all", key)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		var values []string
		for _, line := range strings.Split(string(out), "\n") {
			values = append(values, strings.TrimSpace(line))
		}
		// A trailing newline yields one empty tail element, which is not a
		// value; a genuinely empty value keeps its place.
		if n := len(values); n > 0 && values[n-1] == "" {
			values = values[:n-1]
		}
		return values, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil
	}
	return nil, fmt.Errorf("read local Git setting %s: %w: %s", key, err, strings.TrimSpace(string(out)))
}

func localGitValueAt(ctx context.Context, gitPath, dir, key string) (string, bool, error) {
	values, err := localGitValuesAt(ctx, gitPath, dir, key)
	if err != nil || len(values) == 0 {
		return "", false, err
	}
	return values[len(values)-1], true, nil
}

func gitBooleanTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

// provisionGitIdentity is the sole Git-configuration write path. It writes
// repository-local settings only, and reports the local values it replaced
// rather than refusing to replace them: overwriting an identity the user
// set by hand in this repository is undone in seconds with one
// `git config --local`, so the useful thing is to write and say what changed.
// Setup is serialized per project root so the report of what was replaced
// matches what a concurrent request left behind, while unrelated
// repositories proceed independently. Linked worktrees inherit the primary
// setup and do not perform a second setup write.
func provisionGitIdentity(
	ctx context.Context,
	projectRoot string,
	allowedRoots []string,
	requested GitIdentity,
) (GitIdentity, *ReplacedGitIdentity, error) {
	identity, err := validateGitIdentity(requested)
	if err != nil {
		return GitIdentity{}, nil, err
	}
	roots, err := canonicalRoots(allowedRoots)
	if err != nil {
		return GitIdentity{}, nil, err
	}
	// This is the one path that writes Git configuration, so the .git git
	// would follow is confined before the first invocation and discovery
	// is ceilinged at the roots: a pointer or linked worktree reaching an
	// outside repository is refused rather than written to.
	if !gitDirConfined(projectRoot, roots) {
		return GitIdentity{}, nil, fmt.Errorf("%w: %s belongs to a repository outside the allowed roots", ErrOutsideRoots, projectRoot)
	}
	env := gitCeiling(roots)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return GitIdentity{}, nil, fmt.Errorf("resolve Git executable: %w", err)
	}
	lockValue, _ := gitIdentityLocks.LoadOrStore(filepath.Clean(projectRoot), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	top, err := gitOutputIn(ctx, gitPath, projectRoot, env, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != filepath.Clean(projectRoot) {
		// A fresh project directory becomes a repository as part of setup. If
		// it is already nested inside another repository, rev-parse succeeds
		// and the exact-root check below refuses it instead.
		if _, initErr := gitOutputIn(ctx, gitPath, projectRoot, env, "init", "--quiet", "--initial-branch=main"); initErr != nil {
			return GitIdentity{}, nil, fmt.Errorf("%w: %w", ErrNotGitRepository, initErr)
		}
		top, err = gitOutputIn(ctx, gitPath, projectRoot, env, "rev-parse", "--show-toplevel")
		if err != nil {
			return GitIdentity{}, nil, fmt.Errorf("%w: %w", ErrNotGitRepository, err)
		}
	}
	top, err = filepath.Abs(top)
	if err != nil || filepath.Clean(top) != filepath.Clean(projectRoot) {
		return GitIdentity{}, nil, fmt.Errorf("%w: registered path must be the repository root", ErrNotGitRepository)
	}
	// git init may have just created the .git; confine again what git will
	// now read and write through, in case the entry changed under us.
	if !gitDirConfined(projectRoot, roots) {
		return GitIdentity{}, nil, fmt.Errorf("%w: %s belongs to a repository outside the allowed roots", ErrOutsideRoots, projectRoot)
	}
	wants := []struct{ key, value string }{
		{"user.name", identity.Name},
		{"user.email", identity.Email},
		{"user.useConfigOnly", "true"},
	}
	var replaced ReplacedGitIdentity
	anyReplaced := false
	for _, want := range wants {
		got, readErr := localGitValuesIn(ctx, gitPath, projectRoot, env, want.key)
		if readErr != nil {
			return GitIdentity{}, nil, readErr
		}
		// Several values are always a change: the write collapses them to one.
		matches := len(got) == 1 && got[0] == want.value
		if want.key == "user.useConfigOnly" {
			matches = len(got) == 1 && gitBooleanTrue(got[0])
		}
		if len(got) > 0 && matches {
			continue
		}
		if len(got) > 0 {
			was := strings.Join(got, ", ")
			switch want.key {
			case "user.name":
				replaced.Name = was
			case "user.email":
				replaced.Email = was
			case "user.useConfigOnly":
				replaced.UseConfigOnly = was
			}
			anyReplaced = true
		}
		if _, err := gitOutputIn(ctx, gitPath, projectRoot, env, "config", "--local", "--replace-all", want.key, want.value); err != nil {
			return GitIdentity{}, nil, err
		}
	}
	if !anyReplaced {
		return identity, nil, nil
	}
	return identity, &replaced, nil
}
