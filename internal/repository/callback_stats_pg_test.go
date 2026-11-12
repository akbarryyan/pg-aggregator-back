package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCallbackStatsPG(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewMerchantCallbackRepository(db)

	empty, err := repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Breakdown == nil || len(empty.Breakdown) != 0 || len(empty.DailyByDay) != 0 {
		t.Fatalf("empty = %+v", empty)
	}

	now := time.Now()
	for _, st := range []string{"success", "success", "failed", "pending", "sending", "skipped"} {
		insertPGCallback(t, db, st, 1, now, nil)
	}
	set := func(id uuid.UUID, expr string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE merchant_callback_deliveries SET created_at = `+expr+` WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	atStart := insertPGCallback(t, db, "failed", 1, now, nil)
	set(atStart, "(CURRENT_DATE - 13 * INTERVAL '1 day')")
	justOutside := insertPGCallback(t, db, "success", 1, now, nil)
	set(justOutside, "(CURRENT_DATE - 13 * INTERVAL '1 day' - INTERVAL '1 second')")
	old := insertPGCallback(t, db, "success", 1, now, nil)
	set(old, "now() - INTERVAL '90 days'")

	got, err := repo.Stats(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, b := range got.Breakdown {
		counts[b.Status] = b.Count
	}
	if counts["success"] != 4 || counts["failed"] != 2 || counts["pending"] != 1 || counts["sending"] != 1 || counts["skipped"] != 1 || len(counts) != 5 {
		t.Fatalf("breakdown = %v", counts)
	}
	var today, start string
	if err := db.QueryRow(`SELECT CURRENT_DATE::text, (CURRENT_DATE - 13)::text`).Scan(&today, &start); err != nil {
		t.Fatal(err)
	}
	if g := got.DailyByDay[today]; g != (CallbackDayAgg{Success: 2, Failed: 1, Other: 3}) {
		t.Fatalf("today = %+v, want success 2 failed 1 other 3", g)
	}
	if g := got.DailyByDay[start]; g != (CallbackDayAgg{Failed: 1}) {
		t.Fatalf("window start = %+v, want failed 1", g)
	}
	if len(got.DailyByDay) != 2 {
		t.Fatalf("daily = %v, want 2 days (rows before the window excluded)", got.DailyByDay)
	}
}
