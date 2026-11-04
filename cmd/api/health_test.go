package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

func TestHealthHandler(t *testing.T) {
	tests := []struct {
		name       string
		check      func(context.Context) error
		wantStatus int
		wantBody   string
	}{
		{"healthy", func(context.Context) error { return nil }, http.StatusOK, `{"status":"ok"}`},
		{"database down", func(context.Context) error {
			return errors.New("pq: password authentication failed for user \"secret_user\"")
		}, http.StatusServiceUnavailable, `{"status":"unavailable"}`},
		{"no checker configured fails closed", nil, http.StatusServiceUnavailable, `{"status":"unavailable"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newHealthHandler(tc.check)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
				t.Errorf("body = %s, want %s", got, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret_user") || strings.Contains(rec.Body.String(), "pq:") {
				t.Error("response must never echo the underlying error")
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

func TestHealthHandler_PingHasTwoSecondDeadline(t *testing.T) {
	var got time.Duration
	h := newHealthHandler(func(ctx context.Context) error {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("ping context must carry a deadline")
		}
		got = time.Until(dl)
		return nil
	})
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if got <= 0 || got > healthPingTimeout {
		t.Errorf("deadline in %v, want within (0, %v]", got, healthPingTimeout)
	}
}

func TestRunHealthcheck(t *testing.T) {
	logger.Init("error") // keep test output quiet

	serve := func(status int) (int, func()) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/health" {
				t.Errorf("unexpected path %q", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		u := srv.Listener.Addr().String()
		port, _ := strconv.Atoi(u[strings.LastIndex(u, ":")+1:])
		return port, srv.Close
	}

	t.Run("200 is healthy", func(t *testing.T) {
		port, stop := serve(http.StatusOK)
		defer stop()
		if code := runHealthcheck(port, &http.Client{Timeout: time.Second}); code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})
	t.Run("503 is unhealthy", func(t *testing.T) {
		port, stop := serve(http.StatusServiceUnavailable)
		defer stop()
		if code := runHealthcheck(port, &http.Client{Timeout: time.Second}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
	})
	t.Run("connection refused is unhealthy", func(t *testing.T) {
		port, stop := serve(http.StatusOK)
		stop() // nothing listens on the port any more
		if code := runHealthcheck(port, &http.Client{Timeout: time.Second}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
	})
}
