package tasks

import (
	"reflect"
	"testing"
)

func TestHandoffSocketNameFitsWhereTmuxSocketFits(t *testing.T) {
	id, err := newRunID()
	if err != nil {
		t.Fatal(err)
	}
	const tmuxSocketName = "/noisedeck.sock"
	if got := len("/" + handoffSocketName(id)); got > len(tmuxSocketName) {
		t.Fatalf("handoff socket adds %d bytes to its directory, tmux's socket adds %d", got, len(tmuxSocketName))
	}
}

func TestGenerationEngineArgv(t *testing.T) {
	for _, tc := range []struct {
		goos  string
		count int
		want  []string
	}{
		{"linux", 0, []string{"/engine", "tasks", "generate", "--yes", "/proc/self/fd/10"}},
		{"linux", 2, []string{"/engine", "tasks", "generate", "--yes", "-num-tasks", "2", "/proc/self/fd/10"}},
		{"darwin", 0, []string{"/engine", "tasks", "generate", "--yes", "/dev/fd/10"}},
		{"darwin", 50, []string{"/engine", "tasks", "generate", "--yes", "-num-tasks", "50", "/dev/fd/10"}},
	} {
		if got := generationEngineArgv("/engine", tc.count, tc.goos); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s count %d: got %v, want %v", tc.goos, tc.count, got, tc.want)
		}
	}
}
