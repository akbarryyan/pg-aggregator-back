package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// WebhookEventDayAgg is the provider webhook events received on one day.
type WebhookEventDayAgg struct {
	Processed int64
	Failed    int64
}

// WebhookEventStats is the raw aggregate behind the admin Logs page summary.
//
// Definitions (benign = processing_error equals exactly one of the sentinel
// messages the caller passes in: non-settlement webhook, duplicate webhook,
// payment already terminal, provider test event):
//   - processed = is_processed
//   - ignored   = benign
//   - failed    = NOT benign AND (processing_error IS NOT NULL OR status IN
//     ('rejected','failed'))
//
// ignored and failed are disjoint by construction (benign wins, so a provider
// "failed" webhook that was merely not a settlement counts as ignored). An
// event can be none of them (still "received"). Ignored is only in the totals,
// not in the daily series.
type WebhookEventStats struct {
	Total     int64
	Processed int64
	Failed    int64
	Ignored   int64
	// Breakdown lists the statuses present, ordered by status. Never nil.
	Breakdown []StatusCount
	// DailyByDay maps "2006-01-02" (DATE(created_at)) to that day's events.
	DailyByDay map[string]WebhookEventDayAgg
}

const (
	webhookBenignExpr = `COALESCE(processing_error = ANY($1::text[]), false)`
	webhookFailedExpr = `(NOT ` + webhookBenignExpr + ` AND (processing_error IS NOT NULL OR status IN ('rejected', 'failed')))`
)

// Stats returns the webhook-event aggregates in two light queries: one
// GROUP BY status pass (totals and breakdown) and the daily series. benign
// lists the processing_error messages that are intentional non-failures.
func (r *WebhookEventRepository) Stats(ctx context.Context, days int, benign []string) (*WebhookEventStats, error) {
	if days < 1 {
		days = 1
	}
	if benign == nil {
		benign = []string{}
	}
	s := &WebhookEventStats{Breakdown: []StatusCount{}, DailyByDay: make(map[string]WebhookEventDayAgg)}

	srows, err := r.db.QueryContext(ctx, `
		SELECT status, COUNT(*),
		       COUNT(*) FILTER (WHERE is_processed),
		       COUNT(*) FILTER (WHERE `+webhookFailedExpr+`),
		       COUNT(*) FILTER (WHERE `+webhookBenignExpr+`)
		  FROM webhook_events
		 GROUP BY status
		 ORDER BY status`, pq.Array(benign))
	if err != nil {
		return nil, fmt.Errorf("failed to count webhook events: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var sc StatusCount
		var processed, failed, ignored int64
		if err := srows.Scan(&sc.Status, &sc.Count, &processed, &failed, &ignored); err != nil {
			return nil, fmt.Errorf("failed to scan webhook event counts: %w", err)
		}
		s.Total += sc.Count
		s.Processed += processed
		s.Failed += failed
		s.Ignored += ignored
		s.Breakdown = append(s.Breakdown, sc)
	}
	if err := srows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read webhook event counts: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT DATE(created_at) AS day,
		       COUNT(*) FILTER (WHERE is_processed),
		       COUNT(*) FILTER (WHERE `+webhookFailedExpr+`)
		  FROM webhook_events
		 WHERE created_at >= (CURRENT_DATE - ($2::int - 1) * INTERVAL '1 day')
		 GROUP BY day`, pq.Array(benign), days)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily webhook events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var a WebhookEventDayAgg
		if err := rows.Scan(&day, &a.Processed, &a.Failed); err != nil {
			return nil, fmt.Errorf("failed to scan daily webhook events: %w", err)
		}
		s.DailyByDay[day.Format("2006-01-02")] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read daily webhook events: %w", err)
	}
	return s, nil
}
