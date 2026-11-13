package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
)

const (
	emailCacheTTL      = 5 * time.Minute
	emailLookupTimeout = 2 * time.Second
)

type adminEmailSource interface {
	GetByID(ctx context.Context, id uuid.UUID) (*admin.Admin, error)
}

type merchantUserEmailSource interface {
	GetByID(ctx context.Context, id uuid.UUID) (*merchant.User, error)
}

type cachedEmail struct {
	email   string
	expires time.Time
}

// newEmailLookup resolves actor emails for the activity log, caching results
// (including misses, as "") for emailCacheTTL so a busy admin doesn't cost a
// query per request.
func newEmailLookup(admins adminEmailSource, users merchantUserEmailSource) activity.EmailLookup {
	var cache sync.Map // key: actorType+":"+id -> cachedEmail
	return func(ctx context.Context, actorType string, id uuid.UUID) string {
		key := actorType + ":" + id.String()
		if v, ok := cache.Load(key); ok {
			if c := v.(cachedEmail); time.Now().Before(c.expires) {
				return c.email
			}
		}
		// Detach from the request so the async-bound lookup is not cut short by
		// a cancelled request, but bound it so it can't hang.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emailLookupTimeout)
		defer cancel()
		email := ""
		switch actorType {
		case activity.ActorAdmin:
			a, err := admins.GetByID(ctx, id)
			if err != nil && !errors.Is(err, admin.ErrAdminNotFound) {
				return "" // transient error: do not cache
			}
			if err == nil && a != nil {
				email = a.Email
			}
		case activity.ActorMerchantUser:
			u, err := users.GetByID(ctx, id)
			if err != nil && !errors.Is(err, merchant.ErrMerchantUserNotFound) {
				return ""
			}
			if err == nil && u != nil {
				email = u.Email
			}
		}
		cache.Store(key, cachedEmail{email: email, expires: time.Now().Add(emailCacheTTL)})
		return email
	}
}
