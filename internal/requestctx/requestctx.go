// Package requestctx is a dependency-free home for request-scoped identity
// context keys and the client-IP helper, so packages that sit below
// internal/middleware in the import graph (e.g. internal/activity) can use
// them without an import cycle.
package requestctx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/google/uuid"
)

type Key string

const (
	MerchantIDKey     Key = "merchant_id"
	AdminIDKey        Key = "admin_id"
	MerchantUserIDKey Key = "merchant_user_id"
)

func uuidFrom(ctx context.Context, k Key) (uuid.UUID, bool) {
	id, ok := ctx.Value(k).(uuid.UUID)
	return id, ok
}

func AdminID(ctx context.Context) (uuid.UUID, bool)        { return uuidFrom(ctx, AdminIDKey) }
func MerchantUserID(ctx context.Context) (uuid.UUID, bool) { return uuidFrom(ctx, MerchantUserIDKey) }
func MerchantID(ctx context.Context) (uuid.UUID, bool)     { return uuidFrom(ctx, MerchantIDKey) }

// ClientIP returns the client address of a request. The TCP peer address
// is authoritative unless the peer is in trusted: only then
// is X-Forwarded-For consulted, walked right to left, and the first address
// that isn't itself a trusted proxy is the client. Entries further left were
// supplied by the client and could be forged, so they are never used. Any
// doubt (no header, an unparseable entry, every entry trusted) falls back to
// the peer address.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !IsTrusted(trusted, peer) {
		return host
	}

	var entries []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(v, ",")...)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil {
			return host
		}
		addr = addr.Unmap()
		if !IsTrusted(trusted, addr) {
			return addr.String()
		}
	}
	return host
}

// IsTrusted reports whether addr falls inside any trusted prefix.
func IsTrusted(trusted []netip.Prefix, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
