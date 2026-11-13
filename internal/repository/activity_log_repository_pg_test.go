package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"github.com/google/uuid"
)

func actEntry(actor, action, method string, status int, at time.Time) *activity.Entry {
	return &activity.Entry{
		CreatedAt: at, ActorType: actor, Action: action, Method: method,
		Route: "/api/v1/x/{id}", StatusCode: status,
	}
}

func TestActivityLogRepositoryPG(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewActivityLogRepository(db)
	ctx := context.Background()
	now := time.Now()

	adminID, merchID := uuid.New(), uuid.New()
	e1 := actEntry(activity.ActorAdmin, "Viewed payment", "GET", 200, now.Add(-3*time.Hour))
	e1.ActorID, e1.ActorEmail, e1.TargetID = &adminID, "admin@a.b", "PAY-1"
	e1.Details = map[string]interface{}{"amount": 5000}
	e2 := actEntry(activity.ActorMerchantUser, "Requested payout", "POST", 422, now.Add(-2*time.Hour))
	e2.ActorID, e2.ActorEmail, e2.MerchantID = &merchID, "m@a.b", &merchID
	e3 := actEntry(activity.ActorAnonymous, "Merchant login", "POST", 401, now.Add(-1*time.Hour))
	e3.ActorEmail = "x@y.z"
	for _, e := range []*activity.Entry{e1, e2, e3} {
		if err := repo.Insert(ctx, e); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	old := actEntry(activity.ActorAdmin, "Old", "GET", 200, now.AddDate(0, 0, -20))
	if err := repo.Insert(ctx, old); err != nil {
		t.Fatal(err)
	}

	all, err := repo.List(ctx, activity.Filter{}, 0, 0)
	if err != nil || len(all) != 4 {
		t.Fatalf("list all: %v len=%d", err, len(all))
	}
	if all[0].ActorEmail != "x@y.z" || all[1].ActorEmail != "m@a.b" || all[2].ActorEmail != "admin@a.b" {
		t.Fatalf("order wrong: %+v", all)
	}
	if all[2].Details["amount"] != float64(5000) {
		t.Fatalf("details roundtrip: %v", all[2].Details)
	}
	if all[1].Details == nil || len(all[1].Details) != 0 {
		t.Fatalf("nil details should be {}: %#v", all[1].Details)
	}
	if all[0].ActorID != nil || all[1].MerchantID == nil || *all[1].MerchantID != merchID {
		t.Fatalf("nullable ids wrong")
	}

	from := now.Add(-150 * time.Minute)
	to := now.Add(-90 * time.Minute)
	cases := []struct {
		name string
		f    activity.Filter
		want int
	}{
		{"search email", activity.Filter{Search: "X@Y.z"}, 1},
		{"search target", activity.Filter{Search: "PAY-1"}, 1},
		{"search action", activity.Filter{Search: "payout"}, 1},
		{"actor", activity.Filter{ActorType: activity.ActorAdmin}, 2},
		{"merchant", activity.Filter{MerchantID: &merchID}, 1},
		{"failed", activity.Filter{Result: "failed"}, 2},
		{"success", activity.Filter{Result: "success"}, 2},
		{"range", activity.Filter{DateFrom: &from, DateTo: &to}, 1},
	}
	for _, c := range cases {
		got, err := repo.List(ctx, c.f, 50, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		n, err := repo.Count(ctx, c.f)
		if err != nil {
			t.Fatalf("%s count: %v", c.name, err)
		}
		if len(got) != c.want || n != int64(c.want) {
			t.Errorf("%s: list=%d count=%d want %d", c.name, len(got), n, c.want)
		}
	}
	if p, _ := repo.List(ctx, activity.Filter{}, 2, 3); len(p) != 1 {
		t.Errorf("offset page len=%d want 1", len(p))
	}

	st, err := repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 3 || st.FailedLogins != 1 || st.ActiveActors != 2 || st.Days != 14 {
		t.Fatalf("stats: %+v", st)
	}
	if st.Daily == nil || st.Breakdown == nil {
		t.Fatal("nil slices")
	}
	var adm, mer int64
	for _, d := range st.Daily {
		adm += d.Admin
		mer += d.Merchant
	}
	if adm != 1 || mer != 2 {
		t.Errorf("daily admin=%d merchant=%d", adm, mer)
	}

	// Breakdown: 8 distinct actions -> 6 + Other.
	repo2 := repo
	for i := 0; i < 8; i++ {
		e := actEntry(activity.ActorAdmin, "Action "+string(rune('A'+i)), "GET", 200, now)
		if err := repo2.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	st, err = repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Breakdown) != 7 || st.Breakdown[6].Action != "Other" {
		t.Fatalf("breakdown: %+v", st.Breakdown)
	}
	var sum int64
	for _, b := range st.Breakdown {
		sum += b.Count
	}
	if sum != st.Total {
		t.Errorf("breakdown sum %d != total %d", sum, st.Total)
	}
}

func TestActivityLogPurgePG(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewActivityLogRepository(db)
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 5; i++ {
		if err := repo.Insert(ctx, actEntry(activity.ActorAdmin, "Old", "GET", 200, now.AddDate(0, 0, -100-i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Insert(ctx, actEntry(activity.ActorAdmin, "New", "GET", 200, now)); err != nil {
		t.Fatal(err)
	}
	n, err := repo.PurgeOlderThan(ctx, now.AddDate(0, 0, -90), 2)
	if err != nil || n != 5 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	if c, _ := repo.Count(ctx, activity.Filter{}); c != 1 {
		t.Fatalf("remaining %d", c)
	}
}

func TestActivityLogMigrationIdempotentPG(t *testing.T) {
	_, db := newPGRepo(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "023_create_activity_logs.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
}

func TestActivityLogListForExportPG(t *testing.T) {
	_, db := newPGRepo(t)
	repo := NewActivityLogRepository(db)
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 250; i++ {
		if err := repo.Insert(ctx, actEntry(activity.ActorAdmin, "Viewed", "GET", 200, now.Add(-time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := repo.ListForExport(ctx, activity.Filter{}, 5000)
	if err != nil || len(rows) != 250 {
		t.Fatalf("export all: %v len=%d, want 250", err, len(rows))
	}
	rows, err = repo.ListForExport(ctx, activity.Filter{}, 230)
	if err != nil || len(rows) != 230 {
		t.Fatalf("export limit 230: %v len=%d", err, len(rows))
	}
	// the cap is 5000: larger requests are clamped (verified through the clamp helper)
	if got := clampExportLimit(1_000_000); got != 5000 {
		t.Fatalf("clamp = %d", got)
	}
	if got := clampExportLimit(0); got != 5000 {
		t.Fatalf("clamp(0) = %d", got)
	}
}
