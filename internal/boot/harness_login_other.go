//go:build !linux

package boot

import (
	"context"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/harnesslogin"
)

func (h *Handles) startHarnessLogin(context.Context, *config.Config) *harnesslogin.Service {
	return nil
}
