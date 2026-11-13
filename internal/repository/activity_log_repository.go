package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"github.com/google/uuid"
)

// ActivityLogRepository persists the append-only activity log.
type ActivityLogRepository struct {
	db *sql.DB
}

func NewActivityLogRepository(db *sql.DB) *ActivityLogRepository {
	return &ActivityLogRepository{db: db}
}

// Insert stores e, filling ID and CreatedAt when zero. Nil details are stored as {}.
func (r *ActivityLogRepository) Insert(ctx context.Context, e *activity.Entry) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	details := []byte("{}")
	if e.Details != nil {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("failed to marshal activity details: %w", err)
		}
		details = b
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO activity_logs (id, created_at, actor_type, actor_id, actor_email, merchant_id,
			action, method, route, target_id, status_code, ip, user_agent, duration_ms, details)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		e.ID, e.CreatedAt, e.ActorType, e.ActorID, e.ActorEmail, e.MerchantID,
		e.Action, e.Method, e.Route, e.TargetID, e.StatusCode, e.IP, e.UserAgent, e.DurationMS, details)
	if err != nil {
		return fmt.Errorf("failed to insert activity log: %w", err)
	}
	return nil
}

func activityWhere(f activity.Filter) (string, []interface{}) {
	var conds []string
	var args []interface{}
	add := func(cond string, v interface{}) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
		add("(actor_email ILIKE ? OR action ILIKE ? OR target_id ILIKE ?)", "%"+esc+"%")
	}
	if f.ActorType != "" {
		add("actor_type = ?", f.ActorType)
	}
	if f.MerchantID != nil {
		add("merchant_id = ?", *f.MerchantID)
	}
	switch f.Result {
	case "success":
		conds = append(conds, "status_code < 400")
	case "failed":
		conds = append(conds, "status_code >= 400")
	}
	if f.DateFrom != nil {
		add("created_at >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		add("created_at < ?", *f.DateTo)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const activityExportMax = 5000

// clampExportLimit maps limit<=0 or above the export cap to the cap.
func clampExportLimit(limit int) int {
	if limit <= 0 || limit > activityExportMax {
		return activityExportMax
	}
	return limit
}

// List returns entries newest first. limit<=0 means 20, capped at 200.
func (r *ActivityLogRepository) List(ctx context.Context, f activity.Filter, limit, offset int) ([]activity.Entry, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return r.query(ctx, f, limit, offset)
}

// ListForExport returns up to limit entries (capped at 5000) newest first,
// without any COUNT query.
func (r *ActivityLogRepository) ListForExport(ctx context.Context, f activity.Filter, limit int) ([]activity.Entry, error) {
	return r.query(ctx, f, clampExportLimit(limit), 0)
}

func (r *ActivityLogRepository) query(ctx context.Context, f activity.Filter, limit, offset int) ([]activity.Entry, error) {
	where, args := activityWhere(f)
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, created_at, actor_type, actor_id, actor_email, merchant_id, action, method, route,
			target_id, status_code, ip, user_agent, duration_ms, details
		FROM activity_logs`+where+fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list activity logs: %w", err)
	}
	defer rows.Close()
	out := []activity.Entry{}
	for rows.Next() {
		var e activity.Entry
		var actorID, merchantID uuid.NullUUID
		var raw []byte
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.ActorType, &actorID, &e.ActorEmail, &merchantID, &e.Action,
			&e.Method, &e.Route, &e.TargetID, &e.StatusCode, &e.IP, &e.UserAgent, &e.DurationMS, &raw); err != nil {
			return nil, fmt.Errorf("failed to scan activity log: %w", err)
		}
		if actorID.Valid {
			id := actorID.UUID
			e.ActorID = &id
		}
		if merchantID.Valid {
			id := merchantID.UUID
			e.MerchantID = &id
		}
		e.Details = map[string]interface{}{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &e.Details); err != nil || e.Details == nil {
				e.Details = map[string]interface{}{}
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read activity logs: %w", err)
	}
	return out, nil
}

// Count returns the number of entries matching f.
func (r *ActivityLogRepository) Count(ctx context.Context, f activity.Filter) (int64, error) {
	where, args := activityWhere(f)
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_logs`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count activity logs: %w", err)
	}
	return n, nil
}

// Stats summarises the last `days` days (DATE(created_at) >= CURRENT_DATE-(days-1)).
// Daily holds only days with data; the service fills gaps. Label is left empty.
func (r *ActivityLogRepository) Stats(ctx context.Context, days int) (*activity.Stats, error) {
	if days < 1 {
		days = 1
	}
	s := &activity.Stats{Days: days, Daily: []activity.DailyPoint{}, Breakdown: []activity.ActionCount{}}
	const window = `created_at >= (CURRENT_DATE - ($1::int - 1) * INTERVAL '1 day') AND DATE(created_at) >= CURRENT_DATE - ($1::int - 1)`

	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE actor_type = 'anonymous'),
		       COUNT(DISTINCT actor_id)
		FROM activity_logs WHERE `+window, days).Scan(&s.Total, &s.FailedLogins, &s.ActiveActors); err != nil {
		return nil, fmt.Errorf("failed to compute activity totals: %w", err)
	}

	drows, err := r.db.QueryContext(ctx, `
		SELECT TO_CHAR(DATE(created_at), 'YYYY-MM-DD'),
		       COUNT(*) FILTER (WHERE actor_type = 'admin'),
		       COUNT(*) FILTER (WHERE actor_type <> 'admin')
		FROM activity_logs WHERE `+window+`
		GROUP BY DATE(created_at) ORDER BY DATE(created_at)`, days)
	if err != nil {
		return nil, fmt.Errorf("failed to compute daily activity: %w", err)
	}
	defer drows.Close()
	for drows.Next() {
		var d activity.DailyPoint
		if err := drows.Scan(&d.Date, &d.Admin, &d.Merchant); err != nil {
			return nil, fmt.Errorf("failed to scan daily activity: %w", err)
		}
		s.Daily = append(s.Daily, d)
	}
	if err := drows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read daily activity: %w", err)
	}

	brows, err := r.db.QueryContext(ctx, `
		SELECT action, COUNT(*) FROM activity_logs WHERE `+window+`
		GROUP BY action ORDER BY COUNT(*) DESC, action`, days)
	if err != nil {
		return nil, fmt.Errorf("failed to compute activity breakdown: %w", err)
	}
	defer brows.Close()
	var other int64
	for brows.Next() {
		var a activity.ActionCount
		if err := brows.Scan(&a.Action, &a.Count); err != nil {
			return nil, fmt.Errorf("failed to scan activity breakdown: %w", err)
		}
		if len(s.Breakdown) < 6 {
			s.Breakdown = append(s.Breakdown, a)
		} else {
			other += a.Count
		}
	}
	if err := brows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read activity breakdown: %w", err)
	}
	if other > 0 {
		s.Breakdown = append(s.Breakdown, activity.ActionCount{Action: "Other", Count: other})
	}
	return s, nil
}

// PurgeOlderThan deletes entries older than before in batches and returns the total deleted.
func (r *ActivityLogRepository) PurgeOlderThan(ctx context.Context, before time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 5000
	}
	var total int64
	for {
		res, err := r.db.ExecContext(ctx, `
			DELETE FROM activity_logs WHERE id IN (
				SELECT id FROM activity_logs WHERE created_at < $1 LIMIT $2)`, before, batch)
		if err != nil {
			return total, fmt.Errorf("failed to purge activity logs: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("failed to read purge result: %w", err)
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
