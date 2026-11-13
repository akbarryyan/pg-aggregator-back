package activity

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type holderKey struct{}

// holder carries per-request activity data set by handlers.
type holder struct {
	mu         sync.Mutex
	details    map[string]interface{}
	actorType  string
	actorID    *uuid.UUID
	merchantID *uuid.UUID
	actorEmail string
}

func withHolder(ctx context.Context, h *holder) context.Context {
	return context.WithValue(ctx, holderKey{}, h)
}

func holderFrom(ctx context.Context) *holder {
	h, _ := ctx.Value(holderKey{}).(*holder)
	return h
}

// AddDetails merges kv into the request's activity details. No-op when the
// request is not being recorded.
func AddDetails(ctx context.Context, kv map[string]interface{}) {
	h := holderFrom(ctx)
	if h == nil || len(kv) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.details == nil {
		h.details = make(map[string]interface{}, len(kv))
	}
	for k, v := range kv {
		h.details[k] = v
	}
}

// SetActor overrides the actor of the request's activity entry (used by auth
// handlers, where the actor is only known after the handler ran).
func SetActor(ctx context.Context, actorType string, id *uuid.UUID, email string, merchantID *uuid.UUID) {
	h := holderFrom(ctx)
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actorType, h.actorID, h.actorEmail, h.merchantID = actorType, id, email, merchantID
}
