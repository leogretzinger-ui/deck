package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/rs/zerolog"
)

// Exercise the same engine-backed operations the first-project tutorial uses,
// including mutations through Deck rather than directly through a test CLI.
func TestTasksFirstProjectRoundTrip(t *testing.T) {
	binaries := buildTaskBinaries(t)
	projects, p := newProjectEnv(t)
	h := NewHandler(projects, binaries.noise, zerolog.Nop())
	ctx := context.Background()
	result, err := h.initialize(ctx, p.ID)
	if err != nil || result.AlreadyDone {
		t.Fatalf("initialize: %+v, %v", result, err)
	}
	if err := WritePRDFileWithEngine(ctx, binaries.noise, p.CanonicalPath, "reading-list.md", "# Reading list", false); err != nil {
		t.Fatal(err)
	}
	assertFileBody(t, filepath.Join(p.CanonicalPath, ".tasks", "docs", "reading-list.md"), "# Reading list")
	added, err := h.addTask(ctx, p.ID, AddTaskInput{Title: "Unread filter", Description: "One change", Details: "Leave tests unchanged", Priority: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadTasksWithEngine(ctx, binaries.noise, p.CanonicalPath)
	if err != nil || len(read["main"].Tasks) != 1 || read["main"].Tasks[0].Title != "Unread filter" {
		t.Fatalf("read: %+v, %v", read, err)
	}
	if out, stderr, exit := engineRunAt(t, p.CanonicalPath, binaries.noise, "tasks", "set-status", "--revision", strconv.Itoa(read["main"].Tasks[0].Revision), added.ID, "done"); exit != 0 {
		t.Fatalf("finish task: %d, %s, %s", exit, out, stderr)
	}
	read, err = ReadTasksWithEngine(ctx, binaries.noise, p.CanonicalPath)
	if err != nil || read["main"].Tasks[0].Status != "done" {
		t.Fatalf("refresh status: %+v, %v", read, err)
	}
	if _, err := os.Stat(filepath.Join(p.CanonicalPath, ".git", "noise-tasks", "tasks.sqlite")); err != nil {
		t.Fatal(err)
	}
	result, err = h.initialize(ctx, p.ID)
	if err != nil || !result.AlreadyDone {
		t.Fatalf("repeat initialize: %+v, %v", result, err)
	}
}
