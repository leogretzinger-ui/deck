package harnessbump

import (
	"context"
	"fmt"
	"time"
)

// WaitForSettlement keeps the trusted run alive while CI or a branch update
// settles. GitHub may suppress completion events or drop scheduled runs, so
// returning a successful wait can otherwise leave a green bump unreviewed.
// Every pass reads the current heads and repeats Settle's admission checks.
func WaitForSettlement(ctx context.Context, interval time.Duration, settle func(context.Context) ([]Decision, error)) ([]Decision, error) {
	var decisions []Decision
	for {
		if err := ctx.Err(); err != nil {
			return decisions, fmt.Errorf("settlement did not finish: %w; last decisions: %+v", err, decisions)
		}
		var err error
		decisions, err = settle(ctx)
		if err != nil {
			return decisions, err
		}
		pending := false
		for _, d := range decisions {
			if d.Action == ActionReview {
				return decisions, nil
			}
			pending = pending || d.Action == ActionWait || (d.Action == ActionUpdateBranch && d.Applied)
		}
		if !pending {
			return decisions, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
