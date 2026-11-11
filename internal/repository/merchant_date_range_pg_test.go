package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMerchantRepositoryPG_ListAndCountByCreatedRange(t *testing.T) {
	_, db := newPGRepo(t)
	ctx := context.Background()
	repo := NewMerchantRepository(db)

	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }
	ids := map[int]uuid.UUID{}
	for _, d := range []int{1, 5, 10, 20} {
		id := insertConstraintMerchant(t, db)
		if _, err := db.Exec(`UPDATE merchants SET created_at = $1, phone = '' WHERE id = $2`, day(d), id); err != nil {
			t.Fatal(err)
		}
		ids[d] = id
	}
	from := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	toExclusive := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) // inclusive end date = Nov 10

	cases := []struct {
		name     string
		from, to *time.Time
		want     int
	}{
		{"no range", nil, nil, 4},
		{"from only", &from, nil, 3},
		{"to only", nil, &toExclusive, 3},
		{"from and to (both ends inclusive of the day)", &from, &toExclusive, 2},
	}
	for _, c := range cases {
		items, err := repo.ListAdmin(ctx, "", nil, c.from, c.to, 50, 0)
		if err != nil {
			t.Fatalf("%s: list: %v", c.name, err)
		}
		count, err := repo.CountAdmin(ctx, "", nil, c.from, c.to)
		if err != nil {
			t.Fatalf("%s: count: %v", c.name, err)
		}
		if len(items) != c.want || count != int64(c.want) {
			t.Errorf("%s: list=%d count=%d, want %d", c.name, len(items), count, c.want)
		}
	}

	// A merchant created exactly at the exclusive end is left out; at from it is in.
	if _, err := db.Exec(`UPDATE merchants SET created_at = $1 WHERE id = $2`, toExclusive, ids[20]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE merchants SET created_at = $1 WHERE id = $2`, from, ids[1]); err != nil {
		t.Fatal(err)
	}
	items, err := repo.ListAdmin(ctx, "", nil, &from, &toExclusive, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 { // ids[1]@from, ids[5], ids[10]; ids[20]@toExclusive is excluded
		t.Errorf("boundary: got %d, want 3 (from inclusive, to exclusive)", len(items))
	}
}
