package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// handleLivez reports only process liveness. It must never grow
// dependency checks: a supervisor restarting the process because SQLite
// hiccuped would kill recovery, not help it.
func (s *Server) handleLivez(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadyz is the deep gate: false until the startup sequence has
// flipped readiness — which it does only once the exclusive lock, usable
// storage, tmux inventory, and reconciliation are all in place — and false
// again whenever a registered probe fails afterwards. Failures name the
// probe; details stay in logs.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.ready.Load() {
		http.Error(w, "not ready: startup sequence has not completed", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	errs, panics := runReadinessChecks(ctx, s.checks)
	for i, c := range s.checks {
		if panics[i] != nil {
			panic(panics[i])
		}
		if err := errs[i]; err != nil {
			s.log.Warn().Str("check", c.Name).Err(err).Msg("readiness probe failed")
			http.Error(w, fmt.Sprintf("not ready: %s", c.Name), http.StatusServiceUnavailable)
			return
		}
	}
	_, _ = w.Write([]byte("ok\n"))
}

// runReadinessChecks starts every independent probe together so the endpoint's
// request budget is bounded by the slowest check, not by their accumulated
// runtime. Results retain registration order so a failure names the same check
// regardless of which goroutine finishes first. Panics are returned to the
// request goroutine for the server's recovery middleware to handle.
func runReadinessChecks(ctx context.Context, checks []Check) ([]error, []any) {
	errs := make([]error, len(checks))
	panics := make([]any, len(checks))
	var wg sync.WaitGroup
	wg.Add(len(checks))
	for i, check := range checks {
		go func() {
			defer wg.Done()
			defer func() {
				panics[i] = recover()
			}()
			errs[i] = check.Probe(ctx)
		}()
	}
	wg.Wait()
	return errs, panics
}
