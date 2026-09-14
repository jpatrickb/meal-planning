package domain

import (
	"database/sql"
	"testing"

	"github.com/jpatrickb/meal-planning/internal/store"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustCreateItem(t *testing.T, db *sql.DB, name, category, unit string) int64 {
	t.Helper()
	id, err := store.CreatePantryItem(db, name, category, unit)
	if err != nil {
		t.Fatalf("creating pantry item %q: %v", name, err)
	}
	return id
}
