package activity

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestHolderNoopWithoutHolder(t *testing.T) {
	ctx := context.Background()
	AddDetails(ctx, map[string]interface{}{"a": 1})
	SetActor(ctx, ActorAdmin, nil, "x@y.z", nil)
}

func TestHolderMergeAndOverride(t *testing.T) {
	h := &holder{}
	ctx := withHolder(context.Background(), h)
	AddDetails(ctx, map[string]interface{}{"a": 1, "b": 2})
	AddDetails(ctx, map[string]interface{}{"b": 3, "c": 4})
	if h.details["a"] != 1 || h.details["b"] != 3 || h.details["c"] != 4 {
		t.Fatalf("details = %v", h.details)
	}
	id, mid := uuid.New(), uuid.New()
	SetActor(ctx, ActorMerchantUser, &id, "m@x.io", &mid)
	if h.actorType != ActorMerchantUser || *h.actorID != id || h.actorEmail != "m@x.io" || *h.merchantID != mid {
		t.Fatalf("actor not set: %+v", h)
	}
}
