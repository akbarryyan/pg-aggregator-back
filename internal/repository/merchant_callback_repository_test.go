package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// timeEq matches a time.Time bind argument exactly, so the test pins which
// instant is used for updated_at and for the stale cutoff.
type timeEq struct{ want time.Time }

func (m timeEq) Match(v driver.Value) bool {
	got, ok := v.(time.Time)
	return ok && got.Equal(m.want)
}

func TestMerchantCallbackRepository_ClaimDue(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantCallbackRepository(db)
	now := time.Now().UTC()
	id := uuid.New()
	staleAfter := 2 * time.Minute

	cols := []string{"id", "payment_id", "merchant_id", "event_type", "target_url", "request_payload",
		"attempt_number", "status", "http_status", "response_body", "error_message",
		"delivered_at", "next_retry_at", "created_at", "updated_at"}
	mock.ExpectQuery(`(?s)UPDATE merchant_callback_deliveries.*SET status = \$1, updated_at = \$2.*WHERE id IN \(.*status = \$3 OR \(status = \$1 AND updated_at <= \$4\).*ORDER BY created_at.*LIMIT \$5.*FOR UPDATE SKIP LOCKED.*\).*RETURNING`).
		WithArgs(merchant.CallbackStatusSending, timeEq{now}, merchant.CallbackStatusPending, timeEq{now.Add(-staleAfter)}, 50).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(
			id, uuid.New(), uuid.New(), "payment.paid", "https://x.example/h", []byte(`{"a":1}`),
			1, merchant.CallbackStatusSending, nil, nil, nil, nil, nil, now, now))

	got, err := repo.ClaimDue(context.Background(), now, 50, staleAfter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Status != merchant.CallbackStatusSending {
		t.Fatalf("unexpected claim result: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestMerchantCallbackRepository_RequeueDueFailed(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantCallbackRepository(db)

	mock.ExpectExec(`(?s)WITH due AS \(.*status = \$1 AND next_retry_at IS NOT NULL AND next_retry_at <= \$2.*LIMIT \$3.*FOR UPDATE SKIP LOCKED.*\), cleared AS \(.*UPDATE merchant_callback_deliveries.*SET next_retry_at = NULL.*\)\s*INSERT INTO merchant_callback_deliveries.*attempt_number \+ 1.*`).
		WithArgs(merchant.CallbackStatusFailed, sqlmock.AnyArg(), 50, merchant.CallbackStatusPending).
		WillReturnResult(sqlmock.NewResult(0, 2))

	n, err := repo.RequeueDueFailed(context.Background(), time.Now().UTC(), 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("requeued = %d, want 2", n)
	}
}

// A non-positive limit must fall back to the default batch of 50 rather than
// LIMIT 0 (which would silently starve the queue).
func TestMerchantCallbackRepository_NonPositiveLimitDefaultsTo50(t *testing.T) {
	for _, limit := range []int{0, -1} {
		db, mock := newMockDB(t)
		repo := NewMerchantCallbackRepository(db)

		mock.ExpectQuery(`UPDATE merchant_callback_deliveries`).
			WithArgs(merchant.CallbackStatusSending, sqlmock.AnyArg(), merchant.CallbackStatusPending, sqlmock.AnyArg(), 50).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		if _, err := repo.ClaimDue(context.Background(), time.Now().UTC(), limit, time.Minute); err != nil {
			t.Fatalf("ClaimDue(limit=%d): %v", limit, err)
		}

		mock.ExpectExec(`WITH due AS`).
			WithArgs(merchant.CallbackStatusFailed, sqlmock.AnyArg(), 50, merchant.CallbackStatusPending).
			WillReturnResult(sqlmock.NewResult(0, 0))
		if _, err := repo.RequeueDueFailed(context.Background(), time.Now().UTC(), limit); err != nil {
			t.Fatalf("RequeueDueFailed(limit=%d): %v", limit, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("limit=%d unmet expectations: %v", limit, err)
		}
	}
}

func TestMerchantCallbackRepository_ClearRetrySchedule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
		want     bool
	}{{"due failed row cleared", 1, true}, {"nothing to clear", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			repo := NewMerchantCallbackRepository(db)
			id := uuid.New()
			now := time.Now().UTC()

			mock.ExpectExec(`(?s)UPDATE merchant_callback_deliveries\s+SET next_retry_at = NULL, updated_at = \$2\s+WHERE id = \$1 AND status = \$3 AND next_retry_at IS NOT NULL`).
				WithArgs(id, timeEq{now}, merchant.CallbackStatusFailed).
				WillReturnResult(sqlmock.NewResult(0, tc.affected))

			got, err := repo.ClearRetrySchedule(context.Background(), id, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("changed = %v, want %v", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestMerchantCallbackRepository_ReleaseClaimed(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantCallbackRepository(db)
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	now := time.Now().UTC()
	wantArray, err := pq.Array([]string{ids[0].String(), ids[1].String()}).Value()
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectExec(`(?s)UPDATE merchant_callback_deliveries\s+SET status = \$3, updated_at = \$2\s+WHERE id = ANY\(\$1::uuid\[\]\) AND status = \$4`).
		WithArgs(wantArray, timeEq{now}, merchant.CallbackStatusPending, merchant.CallbackStatusSending).
		WillReturnResult(sqlmock.NewResult(0, 1))

	n, err := repo.ReleaseClaimed(context.Background(), ids, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("released = %d, want 1", n)
	}

	// No ids: no query at all.
	if n, err := repo.ReleaseClaimed(context.Background(), nil, now); err != nil || n != 0 {
		t.Errorf("empty release: n=%d err=%v, want 0, nil", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
