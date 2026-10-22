package workspacehost

import (
	"context"

	"github.com/noisefloor-oss/deck/internal/workspaceruntime"
)

// Import excludes concurrent gateway/key operations before opening the native
// journal. Closing this host deliberately preserves the unlocked volume.
func Import(ctx context.Context, cfg Config) error {
	if cfg.Validate() != nil || cfg.Native.Migration == nil {
		return ErrConfiguration
	}
	h := &Host{cfg: cfg}
	if err := h.acquire(); err != nil {
		return err
	}
	defer h.Close()
	native, err := workspaceruntime.Open(cfg.Native)
	if err != nil {
		return err
	}
	h.native = native
	return native.Import(ctx)
}
