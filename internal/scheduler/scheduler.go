// Package scheduler runs periodic in-process background jobs (payment
// expiry, merchant callback retry) without pulling in an external queue.
// The system-design docs earmark Redis+Asynq for background jobs once the
// system needs real distributed scheduling; a ticker is enough for a single
// API instance and keeps the MVP dependency-free.
package scheduler

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

// RunPeriodic runs task immediately, then again every interval, until ctx is
// cancelled. A panic or error from a single run is logged and does not stop
// subsequent ticks — one bad job run must never take the process down or
// starve the other scheduled jobs.
func RunPeriodic(ctx context.Context, interval time.Duration, name string, task func(context.Context) error) {
	runOnce(ctx, name, task)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.InfofCtx(ctx, "scheduler: stopping %q", name)
			return
		case <-ticker.C:
			runOnce(ctx, name, task)
		}
	}
}

func runOnce(ctx context.Context, name string, task func(context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorfCtx(ctx, "scheduler: job %q panicked: %v", name, r)
		}
	}()

	if err := task(ctx); err != nil {
		logger.ErrorfCtx(ctx, "scheduler: job %q failed: %v", name, err)
	}
}
