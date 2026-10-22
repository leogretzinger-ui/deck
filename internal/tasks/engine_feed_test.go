package tasks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadLiveFeedRequiresClosedIncreasingResult(t *testing.T) {
	tests := []struct {
		name string
		json string
		ok   bool
	}{
		{name: "valid", json: `{"events":[{"seq":3}],"oldestRetainedSeq":1,"truncated":false}`, ok: true},
		{name: "null events", json: `{"events":null,"oldestRetainedSeq":1,"truncated":false}`},
		{name: "non-increasing", json: `{"events":[{"seq":2}],"oldestRetainedSeq":1,"truncated":false}`},
		{name: "unknown outer field", json: `{"events":[],"oldestRetainedSeq":1,"truncated":false,"extra":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootPath := liveReadProject(t)
			if err := os.WriteFile(filepath.Join(rootPath, ".live-feed.json"), []byte(tt.json), 0o600); err != nil {
				t.Fatal(err)
			}
			stub := writeStub(t, `
if [ "$1" = tasks ] && [ "$2" = live ] && [ "$3" = feed ]; then
  cat "$PWD/.live-feed.json"
  exit 0
fi
exit 29
`)
			root, err := openEngineTaskRoot(context.Background(), stub, rootPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			feed, err := readLiveFeed(context.Background(), stub, root, 2)
			if tt.ok {
				if err != nil || liveFeedCursor(feed, 2) != 3 {
					t.Fatalf("feed = %+v, %v", feed, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("feed unexpectedly succeeded: %+v", feed)
			}
		})
	}
}
