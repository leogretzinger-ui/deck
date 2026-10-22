package boot

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Run serves HTTP and keeps the reconciler's cadence until ctx ends. It
// returns when the server has drained.
func (h *Handles) Run(ctx context.Context) error {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.Reconciler.Run(rctx)
	h.Taskmaster.Start(rctx)
	go h.Lanes.Run(rctx)
	if h.previewHTTP == nil {
		return h.Server.Run(ctx)
	}
	errs := make(chan error, 1+len(h.previewListeners))
	go func() { errs <- h.Server.Run(rctx) }()
	for _, listener := range h.previewListeners {
		go func() { errs <- h.previewHTTP.Serve(listener) }()
	}
	var result error
	remaining := 1 + len(h.previewListeners)
	select {
	case result = <-errs:
		remaining--
	case <-ctx.Done():
	}
	cancel()
	if h.previewMetadata != nil {
		_ = h.previewMetadata.Close()
	}
	_ = h.Preview.Close()
	shutdown, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	if err := h.previewHTTP.Shutdown(shutdown); err != nil {
		_ = h.previewHTTP.Close()
		if result == nil {
			result = err
		}
	}
	for ; remaining > 0; remaining-- {
		if err := <-errs; err != nil && !errors.Is(err, http.ErrServerClosed) && result == nil {
			result = err
		}
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}

// Close releases what Up opened, in reverse order. It never touches tmux
// sessions or provider processes.
func (h *Handles) Close() {
	if h.GitHubConnect != nil {
		h.GitHubConnect.Close()
		h.GitHubConnect = nil
	}
	if h.previewMetadata != nil {
		_ = h.previewMetadata.Close()
	}
	if h.Preview != nil {
		_ = h.Preview.Close()
	}
	if h.previewHTTP != nil {
		_ = h.previewHTTP.Close()
	}
	for _, listener := range h.previewListeners {
		_ = listener.Close()
	}
	if h.HarnessLogin != nil {
		_ = h.HarnessLogin.Close()
		h.HarnessLogin = nil
	}
	if h.Projects != nil {
		// Running clones stop with the process; what they wrote stays on
		// disk for the next start to report as it finds it.
		h.Projects.Shutdown()
		h.Projects = nil
	}
	if h.Taskmaster != nil {
		h.Taskmaster.Shutdown()
		h.Taskmaster = nil
	}
	if h.DB != nil {
		if err := h.DB.Close(); err != nil {
			h.log.Warn().Err(err).Msg("closing database")
		}
		h.DB = nil
	}
	if h.Lock != nil {
		if err := h.Lock.Release(); err != nil {
			h.log.Warn().Err(err).Msg("releasing backend lock")
		}
		h.Lock = nil
	}
}
