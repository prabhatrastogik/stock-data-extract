package sqlite

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrations(t *testing.T) {
	db := openTestDB(t)

	tables := []string{"instruments", "extraction_state", "options_extraction_state", "schema_migrations"}
	for _, tbl := range tables {
		var name string
		err := db.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %q not found after migrations", tbl)
		} else if err != nil {
			t.Errorf("check table %q: %v", tbl, err)
		}
	}
}

func TestExtractionStateRoundTrip(t *testing.T) {
	db := openTestDB(t)

	date := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)

	if err := db.SetLastDate("NSE", "INFY", "day", date); err != nil {
		t.Fatal(err)
	}

	got, found, err := db.GetLastDate("NSE", "INFY", "day")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !got.Equal(date) {
		t.Errorf("got %v, want %v", got, date)
	}
}

func TestOptionsExtractionStateRoundTrip(t *testing.T) {
	db := openTestDB(t)

	date := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	if err := db.SetOptionsLastDate("NIFTY", "15min", date); err != nil {
		t.Fatal(err)
	}

	got, found, err := db.GetOptionsLastDate("NIFTY", "15min")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !got.Equal(date) {
		t.Errorf("got %v, want %v", got, date)
	}
}

func TestGetLastDateNotFound(t *testing.T) {
	db := openTestDB(t)
	_, found, err := db.GetLastDate("NSE", "UNKNOWN", "day")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false for unknown symbol")
	}
}
