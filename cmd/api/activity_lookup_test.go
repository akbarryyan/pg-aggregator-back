package main

import (
	"context"
	"errors"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

type fakeAdminSrc struct {
	calls int
	a     *admin.Admin
	err   error
}

func (f *fakeAdminSrc) GetByID(ctx context.Context, id uuid.UUID) (*admin.Admin, error) {
	f.calls++
	return f.a, f.err
}

type fakeUserSrc struct{}

func (fakeUserSrc) GetByID(ctx context.Context, id uuid.UUID) (*merchant.User, error) {
	return nil, nil
}

func TestEmailLookupDoesNotCacheTransientErrors(t *testing.T) {
	src := &fakeAdminSrc{err: errors.New("db down")}
	lookup := newEmailLookup(src, fakeUserSrc{})
	id := uuid.New()
	if got := lookup(context.Background(), activity.ActorAdmin, id); got != "" {
		t.Fatalf("got %q", got)
	}
	src.err, src.a = nil, &admin.Admin{Email: "a@x.io"}
	if got := lookup(context.Background(), activity.ActorAdmin, id); got != "a@x.io" {
		t.Fatalf("second call = %q (error was cached)", got)
	}
	if src.calls != 2 {
		t.Fatalf("calls = %d", src.calls)
	}
	lookup(context.Background(), activity.ActorAdmin, id)
	if src.calls != 2 {
		t.Fatalf("success not cached, calls = %d", src.calls)
	}
}

func TestEmailLookupCachesNotFound(t *testing.T) {
	src := &fakeAdminSrc{err: admin.ErrAdminNotFound}
	lookup := newEmailLookup(src, fakeUserSrc{})
	id := uuid.New()
	lookup(context.Background(), activity.ActorAdmin, id)
	lookup(context.Background(), activity.ActorAdmin, id)
	if src.calls != 1 {
		t.Fatalf("calls = %d, want 1", src.calls)
	}
}
