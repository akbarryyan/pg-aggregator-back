package repository

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// newMockDB returns a sqlmock-backed *sql.DB + mock controller. No live
// Postgres is required — this drives the real repository code (real SQL
// strings, real Scan calls) against a fake driver, catching column-order
// mismatches and query-building bugs that in-memory fakes can't.
func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}
