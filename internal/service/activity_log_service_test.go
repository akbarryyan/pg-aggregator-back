package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
)

type activityRepoFake struct {
	entries []activity.Entry
	total   int64
	stats   *activity.Stats
	err     error

	gotFilter        activity.Filter
	gotLimit, gotOff int
	gotDays          int
}

func (f *activityRepoFake) List(ctx context.Context, flt activity.Filter, limit, offset int) ([]activity.Entry, error) {
	f.gotFilter, f.gotLimit, f.gotOff = flt, limit, offset
	return f.entries, f.err
}
func (f *activityRepoFake) ListForExport(ctx context.Context, flt activity.Filter, limit int) ([]activity.Entry, error) {
	f.gotFilter, f.gotLimit, f.gotOff = flt, limit, 0
	return f.entries, f.err
}
func (f *activityRepoFake) Count(ctx context.Context, flt activity.Filter) (int64, error) {
	return f.total, f.err
}
func (f *activityRepoFake) Stats(ctx context.Context, days int) (*activity.Stats, error) {
	f.gotDays = days
	return f.stats, f.err
}

func TestActivityLogService_List_NeverNil(t *testing.T) {
	f := &activityRepoFake{total: 3}
	items, total, err := NewActivityLogService(f).List(context.Background(), activity.Filter{Search: "x"}, 20, 40)
	if err != nil || total != 3 {
		t.Fatalf("err=%v total=%d", err, total)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want empty non-nil", items)
	}
	if f.gotFilter.Search != "x" || f.gotLimit != 20 || f.gotOff != 40 {
		t.Fatalf("args not forwarded: %+v %d %d", f.gotFilter, f.gotLimit, f.gotOff)
	}
}

func TestActivityLogService_List_Error(t *testing.T) {
	f := &activityRepoFake{err: errors.New("boom")}
	if _, _, err := NewActivityLogService(f).List(context.Background(), activity.Filter{}, 1, 0); err == nil {
		t.Fatal("want error")
	}
}

func TestActivityLogService_Stats_FillsWindow(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	f := &activityRepoFake{stats: &activity.Stats{
		Days: 14, Total: 5,
		Daily: []activity.DailyPoint{{Date: "2026-11-13", Admin: 2, Merchant: 3}, {Date: "2026-11-01", Admin: 9}},
	}}
	svc := NewActivityLogService(f)
	svc.now = func() time.Time { return now }
	s, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.gotDays != 14 || s.Days != 14 || len(s.Daily) != 14 {
		t.Fatalf("days=%d len=%d", f.gotDays, len(s.Daily))
	}
	if s.Daily[0].Date != "2026-10-31" || s.Daily[0].Label != "31 Oct" || s.Daily[0].Admin != 0 {
		t.Fatalf("first = %+v", s.Daily[0])
	}
	last := s.Daily[13]
	if last.Date != "2026-11-13" || last.Label != "13 Nov" || last.Admin != 2 || last.Merchant != 3 {
		t.Fatalf("last = %+v", last)
	}
	if s.Daily[1].Date != "2026-11-01" || s.Daily[1].Admin != 9 {
		t.Fatalf("second = %+v", s.Daily[1])
	}
	if s.Breakdown == nil {
		t.Fatal("breakdown nil")
	}
}
