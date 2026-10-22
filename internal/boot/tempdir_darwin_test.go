//go:build darwin

package boot

import (
	"context"
	"os"
	"testing"

	"github.com/rs/zerolog"
)

func TestBootAcceptsDarwinSystemTemporaryAlias(t *testing.T) {
	info, err := os.Lstat("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Skip("system temporary directory is not an alias")
	}
	h, err := Up(context.Background(), testConfig(t), zerolog.Nop())
	if err != nil {
		t.Fatalf("native boot with the system temporary alias: %v", err)
	}
	h.Close()
}
