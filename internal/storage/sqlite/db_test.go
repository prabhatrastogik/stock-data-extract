package sqlite

import (
	"database/sql"
	"testing"
	"time"

	pq "github.com/prabhatrastogik/stock-data-extract/internal/storage/parquet"
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

func TestFnOEquityInstruments(t *testing.T) {
	db := openTestDB(t)
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Seed: two NSE EQ stocks, one NSE index (Kite uses instrument_type="EQ" + segment="INDICES"),
	// one NFO FUT (underlying = RELIANCE).
	err := db.UpsertInstruments(day, []pq.InstrumentRecord{
		{Token: 1, ExchangeToken: 1, TradingSymbol: "RELIANCE", Exchange: "NSE", InstrumentType: "EQ", Segment: "NSE"},
		{Token: 2, ExchangeToken: 2, TradingSymbol: "IRFC", Exchange: "NSE", InstrumentType: "EQ", Segment: "NSE"},
		{Token: 3, ExchangeToken: 3, TradingSymbol: "NIFTY 50", Exchange: "NSE", InstrumentType: "EQ", Segment: "INDICES"},
		{Token: 4, ExchangeToken: 4, TradingSymbol: "RELIANCE24JULFUT", Name: "RELIANCE", Exchange: "NFO", InstrumentType: "FUT", Segment: "NFO-FUT", Expiry: "2024-07-25"},
	})
	if err != nil {
		t.Fatalf("UpsertInstruments: %v", err)
	}

	recs, err := db.FnOEquityInstruments("NSE", []string{"NFO"})
	if err != nil {
		t.Fatalf("FnOEquityInstruments: %v", err)
	}

	if len(recs) != 1 {
		t.Fatalf("want 1 F&O equity instrument, got %d: %v", len(recs), recs)
	}
	if recs[0].TradingSymbol != "RELIANCE" {
		t.Errorf("want RELIANCE, got %s", recs[0].TradingSymbol)
	}
}

func TestLatestInstrumentsBySegment_ReturnsIndices(t *testing.T) {
	db := openTestDB(t)
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// Kite stores NSE indices with instrument_type="EQ" and segment="INDICES"
	if err := db.UpsertInstruments(day, []pq.InstrumentRecord{
		{Token: 1, TradingSymbol: "RELIANCE", Exchange: "NSE", InstrumentType: "EQ", Segment: "NSE"},
		{Token: 2, TradingSymbol: "NIFTY 50", Exchange: "NSE", InstrumentType: "EQ", Segment: "INDICES"},
		{Token: 3, TradingSymbol: "NIFTY BANK", Exchange: "NSE", InstrumentType: "EQ", Segment: "INDICES"},
	}); err != nil {
		t.Fatal(err)
	}

	recs, err := db.LatestInstrumentsBySegment("NSE", "INDICES")
	if err != nil {
		t.Fatalf("LatestInstrumentsBySegment: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 index instruments, got %d", len(recs))
	}
	for _, r := range recs {
		if r.Segment != "INDICES" {
			t.Errorf("unexpected segment %q for %s", r.Segment, r.TradingSymbol)
		}
	}
}

func TestFnOEquityInstruments_ExcludesIndices(t *testing.T) {
	db := openTestDB(t)
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// NIFTY 50 is stored as EQ/INDICES; its FUT name is "NIFTY" which doesn't match
	// the tradingsymbol, so it should be excluded regardless of the segment guard.
	// RELIANCE matches because tradingsymbol == FUT name.
	if err := db.UpsertInstruments(day, []pq.InstrumentRecord{
		{Token: 1, TradingSymbol: "RELIANCE", Exchange: "NSE", InstrumentType: "EQ", Segment: "NSE"},
		{Token: 2, TradingSymbol: "NIFTY 50", Exchange: "NSE", InstrumentType: "EQ", Segment: "INDICES"},
		{Token: 3, TradingSymbol: "RELIANCE24JULFUT", Name: "RELIANCE", Exchange: "NFO", InstrumentType: "FUT", Segment: "NFO-FUT", Expiry: "2024-07-25"},
		{Token: 4, TradingSymbol: "NIFTY24JULFUT", Name: "NIFTY", Exchange: "NFO", InstrumentType: "FUT", Segment: "NFO-FUT", Expiry: "2024-07-25"},
	}); err != nil {
		t.Fatal(err)
	}

	recs, err := db.FnOEquityInstruments("NSE", []string{"NFO"})
	if err != nil {
		t.Fatalf("FnOEquityInstruments: %v", err)
	}
	if len(recs) != 1 || recs[0].TradingSymbol != "RELIANCE" {
		t.Errorf("want [RELIANCE], got %v", recs)
	}
}

func TestFnOEquityInstruments_EmptyFuturesExchanges(t *testing.T) {
	db := openTestDB(t)
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	err := db.UpsertInstruments(day, []pq.InstrumentRecord{
		{Token: 1, TradingSymbol: "RELIANCE", Exchange: "NSE", InstrumentType: "EQ"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Empty futuresExchanges → falls back to all EQ instruments
	recs, err := db.FnOEquityInstruments("NSE", nil)
	if err != nil {
		t.Fatalf("FnOEquityInstruments: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("want 1 record, got %d", len(recs))
	}
}
