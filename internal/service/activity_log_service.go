package service

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
)

// activityLogStore is the slice of the activity repository the service needs.
type activityLogStore interface {
	List(ctx context.Context, f activity.Filter, limit, offset int) ([]activity.Entry, error)
	ListForExport(ctx context.Context, f activity.Filter, limit int) ([]activity.Entry, error)
	Count(ctx context.Context, f activity.Filter) (int64, error)
	Stats(ctx context.Context, days int) (*activity.Stats, error)
}

// ActivityLogService is the admin read side of the activity log.
type ActivityLogService struct {
	repo activityLogStore
	now  func() time.Time
}

func NewActivityLogService(repo activityLogStore) *ActivityLogService {
	return &ActivityLogService{repo: repo, now: time.Now}
}

// List returns one page of entries (never nil) and the total matching count.
func (s *ActivityLogService) List(ctx context.Context, f activity.Filter, limit, offset int) ([]activity.Entry, int64, error) {
	items, err := s.repo.List(ctx, f, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.Count(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	if items == nil {
		items = []activity.Entry{}
	}
	return items, total, nil
}

// ListForExport returns up to limit entries (never nil) without counting.
func (s *ActivityLogService) ListForExport(ctx context.Context, f activity.Filter, limit int) ([]activity.Entry, error) {
	items, err := s.repo.ListForExport(ctx, f, limit)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []activity.Entry{}
	}
	return items, nil
}

// Stats returns the summary with the 14-day window filled with zero days,
// oldest first.
func (s *ActivityLogService) Stats(ctx context.Context) (*activity.Stats, error) {
	raw, err := s.repo.Stats(ctx, pageStatsDays)
	if err != nil {
		return nil, err
	}
	byDay := make(map[string]activity.DailyPoint, len(raw.Daily))
	for _, d := range raw.Daily {
		byDay[d.Date] = d
	}
	out := *raw
	out.Days = pageStatsDays
	out.Daily = make([]activity.DailyPoint, 0, pageStatsDays)
	for _, d := range statsWindow(pageStatsDays, s.now()) {
		p := byDay[d.Key]
		out.Daily = append(out.Daily, activity.DailyPoint{Date: d.Key, Label: d.Label, Admin: p.Admin, Merchant: p.Merchant})
	}
	if out.Breakdown == nil {
		out.Breakdown = []activity.ActionCount{}
	}
	return &out, nil
}
