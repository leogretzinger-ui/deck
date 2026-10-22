package modelbump

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The live tree must repeat the pin store exactly: the SDLC recipes, the
// dominance-rule example, and the settle workflow's fallback literal are
// the literals a refresh rewrites, and CI must fail when any of them
// drifts — a hand-edit that rewords a recipe would otherwise leave the
// pin store and the document disagreeing about who reviews what.
//
// The public source export ships this package's pure logic but not the
// private review lane's files, so there the guard reports the lane as
// not shipped and the test skips. Every tree that carries the lane —
// including one whose pin store was deleted — must fail instead.
func TestLiveTreeRepeatsThePinStore(t *testing.T) {
	err := VerifyLiveTree(filepath.Join("..", ".."))
	if errors.Is(err, ErrLaneNotShipped) {
		t.Skipf("%s and SDLC.md are outside this tree's boundary (public source export)", TruthPath)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The skip decision is part of the guard's contract: a deleted pin store
// in a private checkout must fail, and only the lane's complete absence
// (the public export shape) exempts a tree.
func TestVerifyLiveTreeSkipDecision(t *testing.T) {
	t.Run("public export shape skips", func(t *testing.T) {
		if err := VerifyLiveTree(t.TempDir()); !errors.Is(err, ErrLaneNotShipped) {
			t.Fatalf("empty tree: %v, want ErrLaneNotShipped", err)
		}
	})
	t.Run("private tree missing only the pin store fails", func(t *testing.T) {
		root := fixtureTree(t)
		if err := os.Remove(filepath.Join(root, TruthPath)); err != nil {
			t.Fatal(err)
		}
		if err := VerifyLiveTree(root); err == nil || errors.Is(err, ErrLaneNotShipped) {
			t.Fatalf("SDLC.md without a pin store: %v, want a hard failure", err)
		}
	})
	t.Run("complete private tree verifies", func(t *testing.T) {
		if err := VerifyLiveTree(fixtureTree(t)); err != nil {
			t.Fatal(err)
		}
	})
}
