package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func liveReadProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tasks", "tasks.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadLiveTasksRequiresLiveResultShape(t *testing.T) {
	tests := []struct {
		name string
		json string
		ok   bool
	}{
		{
			name: "valid",
			json: `{"tag":"master","total":1,"counts":{"pending":1},"tasks":[{"id":"1","title":"one","description":"","status":"pending","revision":2,"claim":{"owner":"worker","acquiredAt":"2026-08-19T00:00:00Z","expiresAt":"2026-08-19T00:05:00Z"}}]}`,
			ok:   true,
		},
		{name: "tag-file shape", json: `{"master":{"tasks":[]}}`},
		{name: "missing revision", json: `{"tag":"master","total":1,"counts":{"pending":1},"tasks":[{"id":"1","title":"one","description":"","status":"pending"}]}`},
		{name: "unknown outer field", json: `{"tag":"master","total":0,"counts":{},"tasks":[],"extra":true}`},
		{name: "null counts", json: `{"tag":"master","total":0,"counts":null,"tasks":[]}`},
		{name: "null tasks", json: `{"tag":"master","total":0,"counts":{},"tasks":null}`},
		{name: "trailing document", json: `{"tag":"master","total":0,"counts":{},"tasks":[]} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := liveReadProject(t)
			if err := os.WriteFile(filepath.Join(root, ".live-list.json"), []byte(tt.json), 0o600); err != nil {
				t.Fatal(err)
			}
			stub := writeStub(t, `
if [ "$1" = tasks ] && [ "$2" = list ] && [ "$3" = --json ]; then
  cat "$PWD/.live-list.json"
  exit 0
fi
exit 29
`)
			tags, err := ReadTasksWithEngine(context.Background(), stub, root)
			if tt.ok {
				if err != nil || tags["master"].Tasks[0].Revision != 2 {
					t.Fatalf("read = %+v, %v", tags, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("read unexpectedly succeeded: %+v", tags)
			}
		})
	}
}

func TestReadLiveTasksAcceptsLargeBoundedList(t *testing.T) {
	root := liveReadProject(t)
	wantDetails := strings.Repeat("x", 128<<10)
	raw, err := json.Marshal(liveListResult{
		Tag:    "master",
		Total:  1,
		Counts: map[string]int{"pending": 1},
		Tasks: []Task{{
			ID:       "1",
			Title:    "one",
			Status:   "pending",
			Details:  wantDetails,
			Revision: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 64<<10 {
		t.Fatalf("fixture size = %d, want more than legacy 64 KiB limit", len(raw))
	}
	if err := os.WriteFile(filepath.Join(root, ".live-list.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := writeStub(t, `
if [ "$1" = tasks ] && [ "$2" = list ] && [ "$3" = --json ]; then
  cat "$PWD/.live-list.json"
  exit 0
fi
exit 29
`)
	tags, err := ReadTasksWithEngine(context.Background(), stub, root)
	if err != nil {
		t.Fatalf("read large live list: %v", err)
	}
	if got := tags["master"].Tasks[0].Details; got != wantDetails {
		t.Fatalf("details length = %d, want %d", len(got), len(wantDetails))
	}
}
