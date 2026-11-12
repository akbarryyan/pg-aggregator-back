package service

import (
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
)

// pageStatsDays is the daily-series window of every admin page summary.
const pageStatsDays = 14

// BreakdownPoint is one slice of a status breakdown chart.
type BreakdownPoint struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

// statsDay is one calendar day of a stats window.
type statsDay struct {
	Key   string // "2006-01-02", matches the repository's day keys
	Label string // "02 Jan"
}

// statsWindow returns the `days` calendar days ending today, oldest first.
func statsWindow(days int, now time.Time) []statsDay {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -(days - 1))
	out := make([]statsDay, 0, days)
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i)
		out = append(out, statsDay{Key: d.Format("2006-01-02"), Label: d.Format("02 Jan")})
	}
	return out
}

// toBreakdown converts repository rows to the response type; never nil.
func toBreakdown(rows []repository.StatusCount) []BreakdownPoint {
	out := make([]BreakdownPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, BreakdownPoint{Status: r.Status, Count: r.Count})
	}
	return out
}
