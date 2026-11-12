package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// StatusCount is one row of a "GROUP BY status" breakdown.
type StatusCount struct {
	Status string
	Count  int64
}

// queryStatusCounts runs a query returning (status, count) rows, ordered by the
// caller's SQL. The result is never nil.
func queryStatusCounts(ctx context.Context, db *sql.DB, query, what string) ([]StatusCount, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to count %s by status: %w", what, err)
	}
	defer rows.Close()
	out := []StatusCount{}
	for rows.Next() {
		var sc StatusCount
		if err := rows.Scan(&sc.Status, &sc.Count); err != nil {
			return nil, fmt.Errorf("failed to scan %s status counts: %w", what, err)
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read %s status counts: %w", what, err)
	}
	return out, nil
}
