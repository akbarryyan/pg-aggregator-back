package activity

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/akbarryyan/pg-aggregator-back/internal/requestctx"
)

// BackgroundHeader marks read-only requests (polling etc.) that must not be
// recorded. It is honoured only for GET and HEAD; state-changing methods
// carrying it are still recorded.
const BackgroundHeader = "X-Background-Request"

// EmailLookup resolves the email of an authenticated actor; may return "".
type EmailLookup func(ctx context.Context, actorType string, id uuid.UUID) string

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps streaming handlers (CSV export) working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// clamp truncates s to at most n runes.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Middleware records one Entry per request. It must run inside the mux router
// and after the auth middleware so identity and route are available. It never
// stores request or response bodies.
func Middleware(rec *Recorder, lookup EmailLookup, trusted []netip.Prefix) func(http.Handler) http.Handler {
	return middleware(rec, lookup, trusted, true)
}

// MiddlewareActorOnly is like Middleware but records nothing when the actor
// never resolved (no context identity and no SetActor). Use it for
// handler-authenticated routes where an unresolved actor means a rejected,
// unauthenticated request; only login routes may produce anonymous entries.
func MiddlewareActorOnly(rec *Recorder, lookup EmailLookup, trusted []netip.Prefix) func(http.Handler) http.Handler {
	return middleware(rec, lookup, trusted, false)
}

func middleware(rec *Recorder, lookup EmailLookup, trusted []netip.Prefix, allowAnonymous bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			background := (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get(BackgroundHeader) == "1"
			if r.Method == http.MethodOptions || background {
				next.ServeHTTP(w, r)
				return
			}
			h := &holder{}
			r = r.WithContext(withHolder(r.Context(), h))
			sw := &statusWriter{ResponseWriter: w}
			start := time.Now()
			next.ServeHTTP(sw, r)
			dur := time.Since(start)
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			e := buildEntry(r, h, status, dur, lookup, trusted)
			if e.ActorType == ActorAnonymous && !allowAnonymous {
				return
			}
			rec.Record(e)
		})
	}
}

func buildEntry(r *http.Request, h *holder, status int, dur time.Duration, lookup EmailLookup, trusted []netip.Prefix) Entry {
	route := r.URL.Path
	if cr := mux.CurrentRoute(r); cr != nil {
		if t, err := cr.GetPathTemplate(); err == nil && t != "" {
			route = t
		}
	}
	vars := mux.Vars(r)
	ctx := r.Context()

	h.mu.Lock()
	hActorType, hActorID, hMerchantID, hEmail := h.actorType, h.actorID, h.merchantID, h.actorEmail
	details := make(map[string]interface{}, len(h.details))
	for k, v := range h.details {
		details[k] = v
	}
	h.mu.Unlock()

	var actorType string
	var actorID, merchantID *uuid.UUID
	email := ""
	if hActorType != "" {
		actorType, actorID, merchantID, email = hActorType, hActorID, hMerchantID, hEmail
	} else if id, ok := requestctx.AdminID(ctx); ok {
		actorType, actorID = ActorAdmin, &id
	} else if id, ok := requestctx.MerchantUserID(ctx); ok {
		actorType, actorID = ActorMerchantUser, &id
		if mid, ok := requestctx.MerchantID(ctx); ok {
			merchantID = &mid
		}
	} else {
		actorType = ActorAnonymous
		if s, ok := details["email"].(string); ok {
			email = s
		}
	}
	if email == "" && actorID != nil && lookup != nil {
		email = lookup(ctx, actorType, *actorID)
	}
	if merchantID == nil && strings.Contains(route, "/merchants/{id}") {
		if id, err := uuid.Parse(vars["id"]); err == nil {
			merchantID = &id
		}
	}

	target := ""
	for _, k := range []string{"id", "keyId", "reference", "slug", "name"} {
		if v := vars[k]; v != "" {
			target = v
			break
		}
	}

	return Entry{
		ID:         uuid.New(),
		CreatedAt:  time.Now().UTC(),
		ActorType:  actorType,
		ActorID:    actorID,
		ActorEmail: clamp(email, 255),
		MerchantID: merchantID,
		Action:     clamp(Label(r.Method, route), 100),
		Method:     clamp(r.Method, 10),
		Route:      clamp(route, 255),
		TargetID:   clamp(target, 100),
		StatusCode: status,
		IP:         clamp(requestctx.ClientIP(r, trusted), 64),
		UserAgent:  clamp(r.UserAgent(), 500),
		DurationMS: int(dur / time.Millisecond),
		Details:    details,
	}
}
