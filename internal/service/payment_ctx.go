package service

import (
	"context"
	"time"
)

// postCommitTimeout bounds the work that must finish once a status change is
// about to be / has been committed (the update itself, event bookkeeping,
// recording the merchant callback) even if the caller's context is gone.
const postCommitTimeout = 30 * time.Second

// detached returns a context that keeps ctx's values (request id for logs)
// but not its cancellation, bounded by postCommitTimeout.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), postCommitTimeout)
}
