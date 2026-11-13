package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/akbarryyan/pg-aggregator-back/internal/requestctx"
)

// run serves one request through a mux router with the middleware and returns recorded entries.
func run(t *testing.T, trusted []netip.Prefix, lookup EmailLookup, inject func(*http.Request) *http.Request, h http.HandlerFunc, req *http.Request) []Entry {
	t.Helper()
	w := &fakeWriter{}
	rec := NewRecorder(w, 16)
	r := mux.NewRouter()
	s := r.PathPrefix("/api/v1").Subrouter()
	s.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if inject != nil {
				r = inject(r)
			}
			next.ServeHTTP(w, r)
		})
	})
	s.Use(Middleware(rec, lookup, trusted))
	s.HandleFunc("/admin/payments/{id}", h).Methods("GET", "POST", "OPTIONS")
	r.ServeHTTP(httptest.NewRecorder(), req)
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w.all()
}

func adminCtx(id uuid.UUID) func(*http.Request) *http.Request {
	return func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), requestctx.AdminIDKey, id))
	}
}

func TestMiddlewareAdmin(t *testing.T) {
	id := uuid.New()
	lookup := func(_ context.Context, typ string, got uuid.UUID) string {
		if typ == ActorAdmin && got == id {
			return "a@x.io"
		}
		return ""
	}
	req := httptest.NewRequest("GET", "/api/v1/admin/payments/abc", nil)
	req.Header.Set("User-Agent", "ua")
	es := run(t, nil, lookup, adminCtx(id), func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, req)
	if len(es) != 1 {
		t.Fatalf("entries = %d", len(es))
	}
	e := es[0]
	if e.ActorType != ActorAdmin || *e.ActorID != id || e.ActorEmail != "a@x.io" ||
		e.Route != "/api/v1/admin/payments/{id}" || e.TargetID != "abc" || e.StatusCode != 404 ||
		e.Method != "GET" || e.Action != "Viewed payment" || e.UserAgent != "ua" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestMiddlewareMerchantUser(t *testing.T) {
	uid, mid := uuid.New(), uuid.New()
	inj := func(r *http.Request) *http.Request {
		ctx := context.WithValue(r.Context(), requestctx.MerchantUserIDKey, uid)
		ctx = context.WithValue(ctx, requestctx.MerchantIDKey, mid)
		return r.WithContext(ctx)
	}
	es := run(t, nil, nil, inj, func(w http.ResponseWriter, r *http.Request) {},
		httptest.NewRequest("GET", "/api/v1/admin/payments/1", nil))
	e := es[0]
	if e.ActorType != ActorMerchantUser || *e.ActorID != uid || e.MerchantID == nil || *e.MerchantID != mid || e.StatusCode != 200 {
		t.Fatalf("entry = %+v", e)
	}
}

func TestMiddlewareSkips(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/payments/1", nil)
	req.Header.Set(BackgroundHeader, "1")
	if es := run(t, nil, nil, nil, func(http.ResponseWriter, *http.Request) {}, req); len(es) != 0 {
		t.Fatalf("background recorded: %d", len(es))
	}
	req = httptest.NewRequest("OPTIONS", "/api/v1/admin/payments/1", nil)
	if es := run(t, nil, nil, nil, func(http.ResponseWriter, *http.Request) {}, req); len(es) != 0 {
		t.Fatalf("options recorded: %d", len(es))
	}
}

func TestMiddlewareBackgroundHeaderOnlyForGetHead(t *testing.T) {
	noop := func(http.ResponseWriter, *http.Request) {}
	req := httptest.NewRequest("POST", "/api/v1/admin/payments/1", nil)
	req.Header.Set(BackgroundHeader, "1")
	if es := run(t, nil, nil, nil, noop, req); len(es) != 1 {
		t.Fatalf("POST with background header must be recorded, got %d", len(es))
	}
}

func TestMiddlewareDetailsAndNoBodyLeak(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/admin/payments/1", strings.NewReader(`{"password":"secret"}`))
	es := run(t, nil, nil, nil, func(w http.ResponseWriter, r *http.Request) {
		AddDetails(r.Context(), map[string]interface{}{"amount": 1})
	}, req)
	e := es[0]
	if e.Details["amount"] != 1 {
		t.Fatalf("details = %v", e.Details)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "password") {
		t.Fatalf("body leaked: %s", b)
	}
}

func TestMiddlewareSetActorOverrides(t *testing.T) {
	ctxID, newID := uuid.New(), uuid.New()
	es := run(t, nil, nil, adminCtx(ctxID), func(w http.ResponseWriter, r *http.Request) {
		SetActor(r.Context(), ActorMerchantUser, &newID, "n@x.io", nil)
	}, httptest.NewRequest("GET", "/api/v1/admin/payments/1", nil))
	e := es[0]
	if e.ActorType != ActorMerchantUser || *e.ActorID != newID || e.ActorEmail != "n@x.io" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestMiddlewareAnonymousEmailFromDetails(t *testing.T) {
	es := run(t, nil, nil, nil, func(w http.ResponseWriter, r *http.Request) {
		AddDetails(r.Context(), map[string]interface{}{"email": "who@x.io"})
	}, httptest.NewRequest("GET", "/api/v1/admin/payments/1", nil))
	if es[0].ActorType != ActorAnonymous || es[0].ActorEmail != "who@x.io" || es[0].ActorID != nil {
		t.Fatalf("entry = %+v", es[0])
	}
}

func TestMiddlewareForwardedFor(t *testing.T) {
	mk := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/admin/payments/1", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		return r
	}
	h := func(http.ResponseWriter, *http.Request) {}
	es := run(t, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, nil, nil, h, mk())
	if es[0].IP != "203.0.113.9" {
		t.Fatalf("trusted ip = %q", es[0].IP)
	}
	es = run(t, nil, nil, nil, h, mk())
	if es[0].IP != "10.0.0.1" {
		t.Fatalf("untrusted ip = %q", es[0].IP)
	}
}

func TestMiddlewareClampsLongFields(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/payments/"+strings.Repeat("é", 300), nil)
	req.Header.Set("User-Agent", strings.Repeat("ü", 600))
	lookup := func(context.Context, string, uuid.UUID) string { return strings.Repeat("e", 400) }
	es := run(t, nil, lookup, adminCtx(uuid.New()), func(http.ResponseWriter, *http.Request) {}, req)
	e := es[0]
	if n := len([]rune(e.UserAgent)); n != 500 {
		t.Fatalf("ua runes = %d", n)
	}
	if len([]rune(e.ActorEmail)) != 255 || len([]rune(e.TargetID)) != 100 {
		t.Fatalf("email/target = %d/%d", len([]rune(e.ActorEmail)), len([]rune(e.TargetID)))
	}
}

func TestClampRoute(t *testing.T) {
	if got := clamp(strings.Repeat("x", 300), 255); len(got) != 255 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestMerchantIDFromAdminRoute(t *testing.T) {
	mid := uuid.New()
	w := &fakeWriter{}
	rec := NewRecorder(w, 4)
	r := mux.NewRouter()
	s := r.PathPrefix("/api/v1").Subrouter()
	s.Use(Middleware(rec, nil, nil))
	s.HandleFunc("/admin/merchants/{id}/status", func(http.ResponseWriter, *http.Request) {}).Methods("PUT")
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/api/v1/admin/merchants/"+mid.String()+"/status", nil))
	_ = rec.Close(context.Background())
	if e := w.all()[0]; e.MerchantID == nil || *e.MerchantID != mid {
		t.Fatalf("merchant id = %v", e.MerchantID)
	}
}

func TestLabels(t *testing.T) {
	if Label("POST", "/api/v1/admin/payouts/{id}/pay") != "Paid payout" {
		t.Fatal("label")
	}
	if Label("GET", "/nope") != "GET /nope" || Known("GET", "/nope") {
		t.Fatal("fallback")
	}
}
