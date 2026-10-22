package harnessbump

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// WithMergeLift extends an Evidence across the branch updates settle
// itself makes. Bringing an approved branch up to date creates a merge
// commit no bump run recorded, so the lifted Evidence accepts a head that
// is exactly the mechanical merge of an evidenced tip with a commit on
// main: two parents, the first an ancestor of main's checkout at root,
// and a tree identical to what merging them produces, so the merge added
// nothing of its own. The lift follows such merges up to a few levels.
func WithMergeLift(root string, base Evidence) Evidence {
	var lifted Evidence
	lifted = func(ctx context.Context, h Harness, version, head string) (bool, error) {
		return lift(ctx, root, base, lifted, h, version, head, 0)
	}
	return lifted
}

const maxLift = 8

func lift(ctx context.Context, root string, base, lifted Evidence, h Harness, version, head string, depth int) (bool, error) {
	ok, err := base(ctx, h, version, head)
	if err != nil || ok || depth >= maxLift {
		return ok, err
	}
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := git("fetch", "--quiet", "origin", head); err != nil {
		return false, fmt.Errorf("harnessbump: lift: fetch %s: %w", head, err)
	}
	parents, err := git("rev-list", "--parents", "-n", "1", head)
	if err != nil {
		return false, fmt.Errorf("harnessbump: lift: parents of %s: %w", head, err)
	}
	fields := strings.Fields(parents)
	if len(fields) != 3 {
		return false, nil
	}
	first, second := fields[1], fields[2]
	// The first parent is the branch the update was applied to; the
	// second is what came from main. GitHub records it that way, but the
	// mechanical-merge check below is symmetric, so either order is held
	// to the same rule: one parent on main, the other evidenced.
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		onMain, tip := pair[0], pair[1]
		if err := exec.CommandContext(ctx, "git", "-C", root, "merge-base", "--is-ancestor", onMain, "HEAD").Run(); err != nil {
			continue
		}
		merged, err := git("merge-tree", "--write-tree", onMain, tip)
		if err != nil {
			// A conflicting merge writes no tree: not a mechanical update.
			continue
		}
		tree, err := git("rev-parse", head+"^{tree}")
		if err != nil {
			return false, err
		}
		if strings.Fields(merged)[0] != tree {
			continue
		}
		ok, err := lift(ctx, root, base, lifted, h, version, tip, depth+1)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}
