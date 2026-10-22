package lane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gitTimeout = 30 * time.Second

// Git is the slice of Git the lane needs: keeping the engine's worktree
// directory out of status, and telling whether a worktree holds work that
// closing the lane would lose.
type Git interface {
	// EnsureWorktreesIgnored makes the family's .worktrees directory
	// ignored locally. The engine refuses to provision into a tree where it
	// would show up as untracked.
	EnsureWorktreesIgnored(ctx context.Context, family string) error
	// DeleteBranch deletes a local branch. Without force, Git deletes it only
	// when it is merged.
	DeleteBranch(ctx context.Context, family, branch string, force bool) error
	// Inspect reports the work a worktree holds that is not safe elsewhere.
	Inspect(ctx context.Context, path, branch, base string) (WorktreeState, error)
}

// WorktreeState is what closing a lane would put at risk.
type WorktreeState struct {
	// Missing is a worktree directory that is already gone.
	Missing bool
	// Dirty counts files with uncommitted changes, untracked ones included.
	Dirty int
	// Unpushed is set when commits on the branch exist nowhere else.
	Unpushed bool
	// Detail says why Unpushed is set.
	Detail string
	// Synced is set when the branch has commits and its remote branch has all
	// of them, so deleting the local branch loses nothing.
	Synced bool
}

type execGit struct{}

func (execGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return stdout.String(), err
	}
	return stdout.String(), nil
}

func (g execGit) EnsureWorktreesIgnored(ctx context.Context, family string) error {
	if _, err := g.run(ctx, family, "check-ignore", "-q", ".worktrees/lane"); err == nil {
		return nil
	} else if exit := (&exec.ExitError{}); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return fmt.Errorf("check ignore rules: %w", err)
	}
	out, err := g.run(ctx, family, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("find exclude file: %w", err)
	}
	path := filepath.Clean(strings.TrimSpace(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(family, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	existing, err := os.ReadFile(path) //nolint:gosec // git named this path under the repository it reports
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	line := ".worktrees/\n"
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		line = "\n" + line
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // git named this path under the repository it reports
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (g execGit) DeleteBranch(ctx context.Context, family, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := g.run(ctx, family, "branch", flag, branch)
	return err
}

func (g execGit) Inspect(ctx context.Context, path, branch, base string) (WorktreeState, error) {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return WorktreeState{Missing: true}, nil
		}
		return WorktreeState{}, err
	}
	status, err := g.run(ctx, path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return WorktreeState{}, fmt.Errorf("read worktree status: %w", err)
	}
	state := WorktreeState{}
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) != "" {
			state.Dirty++
		}
	}
	head, err := g.run(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return WorktreeState{}, fmt.Errorf("read worktree head: %w", err)
	}
	head = strings.TrimSpace(head)
	ref, err := g.run(ctx, path, "for-each-ref",
		"--format=%(objectname)%09%(upstream)%09%(upstream:track)", "refs/heads/"+branch)
	if err != nil {
		return WorktreeState{}, fmt.Errorf("read branch: %w", err)
	}
	fields := strings.Split(strings.TrimRight(ref, "\n"), "\t")
	switch {
	case strings.TrimSpace(ref) == "" || len(fields) != 3:
		state.Unpushed, state.Detail = true, "the branch no longer exists in this worktree"
	case head != fields[0]:
		state.Unpushed, state.Detail = true, "the worktree is not on its branch"
	case fields[0] == base:
		// Nothing has been committed on the branch.
	case fields[1] == "":
		state.Unpushed, state.Detail = true, "the branch has commits and was never pushed"
	case strings.Contains(fields[2], "ahead"):
		state.Unpushed, state.Detail = true, "the branch has commits that are not pushed"
	case fields[2] == "":
		state.Synced = true
	}
	return state, nil
}
