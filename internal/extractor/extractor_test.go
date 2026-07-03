package extractor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prabhatrastogik/stock-data-extract/internal/config"
	"github.com/prabhatrastogik/stock-data-extract/internal/provider"
	pq "github.com/prabhatrastogik/stock-data-extract/internal/storage/parquet"
	"github.com/prabhatrastogik/stock-data-extract/internal/storage/sqlite"
)

// ---- fakes ----

type historicalCall struct {
	token    string
	interval string
	from, to time.Time
}

type fakeProvider struct {
	instruments     map[string][]provider.Instrument
	candles         []provider.Candle
	histErr         error
	validateErr     error
	historicalCalls []historicalCall
}

func (f *fakeProvider) Instruments(_ context.Context, exchange string) ([]provider.Instrument, error) {
	return f.instruments[exchange], nil
}

func (f *fakeProvider) Historical(_ context.Context, token, interval string, from, to time.Time, _, _ bool) ([]provider.Candle, error) {
	f.historicalCalls = append(f.historicalCalls, historicalCall{token, interval, from, to})
	if f.histErr != nil {
		return nil, f.histErr
	}
	return f.candles, nil
}

func (f *fakeProvider) ValidateToken(_ context.Context) error { return f.validateErr }
func (f *fakeProvider) SetAccessToken(_ string)               {}

type fakeBlobStore struct {
	data map[string][]byte
}

func newFakeBlob() *fakeBlobStore { return &fakeBlobStore{data: make(map[string][]byte)} }

func (f *fakeBlobStore) Upload(_ context.Context, key string, data []byte, _ string) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	f.data[key] = cp
	return nil
}

func (f *fakeBlobStore) Download(_ context.Context, key string) ([]byte, error) {
	d, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(d))
	copy(cp, d)
	return cp, nil
}

func openTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testCfg() *config.Config {
	return &config.Config{
		Extraction: config.ExtractionConfig{
			Equity: config.AssetConfig{
				Exchanges:    []string{"NSE"},
				Intervals:    []string{"day"},
				BackfillFrom: map[string]string{"day": "2024-01-01"},
			},
			Futures: config.AssetConfig{
				Exchanges:    []string{"NFO"},
				Intervals:    []string{"day"},
				BackfillFrom: map[string]string{"day": "2024-01-01"},
			},
			Options: config.OptionsConfig{
				Exchanges:    []string{"NFO"},
				Underlyings:  []string{"NIFTY"},
				Intervals:    []string{"day"},
				BackfillFrom: map[string]string{"day": "2024-01-01"},
			},
		},
		Kite: config.KiteConfig{
			ChunkDays: map[string]int{"day": 365, "15min": 60},
		},
	}
}

func seedEquityInstrument(t *testing.T, db *sqlite.DB) {
	t.Helper()
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	err := db.UpsertInstruments(day, []pq.InstrumentRecord{
		{Token: 12345, ExchangeToken: 1, TradingSymbol: "RELIANCE", Exchange: "NSE", InstrumentType: "EQ", Segment: "NSE"},
	})
	if err != nil {
		t.Fatalf("seed instruments: %v", err)
	}
}

// ---- pure function tests ----

func TestChunkDateRange_SingleChunk(t *testing.T) {
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)
	chunks := chunkDateRange(from, to, 365)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if !chunks[0][0].Equal(from) || !chunks[0][1].Equal(to) {
		t.Errorf("unexpected chunk: %v", chunks[0])
	}
}

func TestChunkDateRange_MultipleChunks(t *testing.T) {
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
	chunks := chunkDateRange(from, to, 60)
	if len(chunks) < 2 {
		t.Fatalf("want multiple chunks, got %d", len(chunks))
	}
	for i := 1; i < len(chunks); i++ {
		expected := chunks[i-1][1].AddDate(0, 0, 1)
		if !chunks[i][0].Equal(expected) {
			t.Errorf("gap between chunk %d and %d: chunk[%d].end=%v chunk[%d].start=%v",
				i-1, i, i-1, chunks[i-1][1], i, chunks[i][0])
		}
	}
	if !chunks[len(chunks)-1][1].Equal(to) {
		t.Errorf("last chunk should end at %v, got %v", to, chunks[len(chunks)-1][1])
	}
}

func TestChunkDateRange_SameDay(t *testing.T) {
	d := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	chunks := chunkDateRange(d, d, 365)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if !chunks[0][0].Equal(d) || !chunks[0][1].Equal(d) {
		t.Error("single-day chunk should span the same day")
	}
}

func TestSplitByYear_SingleYear(t *testing.T) {
	from := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 11, 30, 0, 0, 0, 0, time.UTC)
	parts := splitByYear(from, to)
	if len(parts) != 1 {
		t.Fatalf("want 1 part, got %d", len(parts))
	}
	if !parts[0][0].Equal(from) || !parts[0][1].Equal(to) {
		t.Errorf("unexpected part: %v", parts[0])
	}
}

func TestSplitByYear_CrossesYearBoundary(t *testing.T) {
	from := time.Date(2023, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 2, 28, 0, 0, 0, 0, time.UTC)
	parts := splitByYear(from, to)
	if len(parts) != 2 {
		t.Fatalf("want 2 parts, got %d", len(parts))
	}
	wantEnd2023 := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
	if !parts[0][1].Equal(wantEnd2023) {
		t.Errorf("first part should end 2023-12-31, got %v", parts[0][1])
	}
	wantStart2024 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if !parts[1][0].Equal(wantStart2024) {
		t.Errorf("second part should start 2024-01-01, got %v", parts[1][0])
	}
	if !parts[1][1].Equal(to) {
		t.Errorf("second part should end at to=%v, got %v", to, parts[1][1])
	}
}

func TestSplitByYear_MultipleYears(t *testing.T) {
	from := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
	parts := splitByYear(from, to)
	if len(parts) != 3 {
		t.Fatalf("want 3 parts (2022, 2023, 2024), got %d", len(parts))
	}
}

func TestSplitByMonth_SingleMonth(t *testing.T) {
	from := time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 3, 20, 0, 0, 0, 0, time.UTC)
	parts := splitByMonth(from, to)
	if len(parts) != 1 {
		t.Fatalf("want 1 part, got %d", len(parts))
	}
	if !parts[0][0].Equal(from) || !parts[0][1].Equal(to) {
		t.Errorf("unexpected part: %v", parts[0])
	}
}

func TestSplitByMonth_CrossesMonthBoundary(t *testing.T) {
	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 3, 10, 0, 0, 0, 0, time.UTC)
	parts := splitByMonth(from, to)
	if len(parts) != 3 {
		t.Fatalf("want 3 parts (Jan, Feb, Mar), got %d", len(parts))
	}
	wantJanEnd := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
	if !parts[0][1].Equal(wantJanEnd) {
		t.Errorf("Jan part should end 2024-01-31, got %v", parts[0][1])
	}
	wantFebStart := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if !parts[1][0].Equal(wantFebStart) {
		t.Errorf("Feb part should start 2024-02-01, got %v", parts[1][0])
	}
	if !parts[2][1].Equal(to) {
		t.Errorf("Mar part should end at to=%v, got %v", to, parts[2][1])
	}
}

func TestLastTradingDay_Tuesday(t *testing.T) {
	tue := time.Date(2024, 1, 9, 0, 0, 0, 0, time.UTC)
	got := lastTradingDay(tue)
	want := time.Date(2024, 1, 8, 0, 0, 0, 0, time.UTC) // Monday
	if !got.Equal(want) {
		t.Errorf("Tuesday: want Monday %v, got %v", want, got)
	}
}

func TestLastTradingDay_Monday(t *testing.T) {
	// Monday → yesterday is Sunday → skip back 2 → Friday
	mon := time.Date(2024, 1, 8, 0, 0, 0, 0, time.UTC)
	got := lastTradingDay(mon)
	want := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC) // Friday
	if !got.Equal(want) {
		t.Errorf("Monday: want Friday %v, got %v", want, got)
	}
}

func TestLastTradingDay_Sunday(t *testing.T) {
	// Sunday → yesterday is Saturday → skip back 1 → Friday
	sun := time.Date(2024, 1, 7, 0, 0, 0, 0, time.UTC)
	got := lastTradingDay(sun)
	want := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC) // Friday
	if !got.Equal(want) {
		t.Errorf("Sunday: want Friday %v, got %v", want, got)
	}
}

func TestLastTradingDay_Saturday(t *testing.T) {
	// Saturday → yesterday is Friday → return Friday
	sat := time.Date(2024, 1, 6, 0, 0, 0, 0, time.UTC)
	got := lastTradingDay(sat)
	want := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC) // Friday
	if !got.Equal(want) {
		t.Errorf("Saturday: want Friday %v, got %v", want, got)
	}
}

func TestFilterByUnderlying_MatchesPrefix(t *testing.T) {
	records := []pq.InstrumentRecord{
		{TradingSymbol: "NIFTY24DEC18000CE"},
		{TradingSymbol: "NIFTY24DEC18000PE"},
		{TradingSymbol: "BANKNIFTY24DEC45000CE"},
		{TradingSymbol: "RELIANCE24DEC3000CE"},
	}
	got := filterByUnderlying("NIFTY", records)
	if len(got) != 2 {
		t.Fatalf("want 2 NIFTY contracts, got %d: %v", len(got), got)
	}
	for _, r := range got {
		if r.TradingSymbol != "NIFTY24DEC18000CE" && r.TradingSymbol != "NIFTY24DEC18000PE" {
			t.Errorf("unexpected symbol: %s", r.TradingSymbol)
		}
	}
}

func TestFilterByUnderlying_Empty(t *testing.T) {
	records := []pq.InstrumentRecord{
		{TradingSymbol: "BANKNIFTY24DEC45000CE"},
	}
	got := filterByUnderlying("NIFTY", records)
	if len(got) != 0 {
		t.Errorf("want 0 results, got %d", len(got))
	}
}

func TestCandlesToRecords(t *testing.T) {
	ts := time.Date(2024, 1, 15, 9, 15, 0, 0, time.UTC)
	candles := []provider.Candle{
		{Time: ts, Open: 100.5, High: 110.0, Low: 90.25, Close: 105.75, Volume: 1000, OI: 500},
	}
	records := candlesToRecords(candles)
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	r := records[0]
	if r.Timestamp != ts.UnixMicro() {
		t.Errorf("timestamp: want %d, got %d", ts.UnixMicro(), r.Timestamp)
	}
	if r.Open != 100.5 || r.High != 110.0 || r.Low != 90.25 || r.Close != 105.75 {
		t.Errorf("OHLC mismatch: got %+v", r)
	}
	if r.Volume != 1000 || r.OI != 500 {
		t.Errorf("volume/OI mismatch: got volume=%d OI=%d", r.Volume, r.OI)
	}
}

func TestInstrumentsToParquet_ExpiryFormatting(t *testing.T) {
	expiry := time.Date(2024, 12, 26, 0, 0, 0, 0, time.UTC)
	insts := []provider.Instrument{
		{
			Token: "12345", ExchangeToken: 1, TradingSymbol: "NIFTY24DEC18000CE",
			Exchange: "NFO", InstrumentType: "CE", Expiry: expiry, Strike: 18000,
		},
		{
			Token: "67890", TradingSymbol: "RELIANCE", Exchange: "NSE", InstrumentType: "EQ",
		},
	}
	records := instrumentsToParquet(insts)
	if len(records) != 2 {
		t.Fatalf("want 2 records, got %d", len(records))
	}
	if records[0].Expiry != "2024-12-26" {
		t.Errorf("option expiry: want 2024-12-26, got %q", records[0].Expiry)
	}
	if records[1].Expiry != "" {
		t.Errorf("equity expiry should be empty, got %q", records[1].Expiry)
	}
	if records[0].Token != 12345 {
		t.Errorf("want token 12345, got %d", records[0].Token)
	}
	if records[0].ExchangeToken != 1 {
		t.Errorf("want exchange_token 1, got %d", records[0].ExchangeToken)
	}
}

func TestInstrumentsToParquet_InvalidToken(t *testing.T) {
	insts := []provider.Instrument{
		{Token: "not-a-number", TradingSymbol: "BAD", Exchange: "NSE"},
	}
	records := instrumentsToParquet(insts) // should not panic; logs warning
	if len(records) != 1 {
		t.Fatalf("want 1 record even on bad token, got %d", len(records))
	}
	if records[0].Token != 0 {
		t.Errorf("bad token should parse to 0, got %d", records[0].Token)
	}
}

// ---- integration tests ----

func TestBackfiller_UnknownType(t *testing.T) {
	b := NewBackfiller(&fakeProvider{}, newFakeBlob(), openTestDB(t), testCfg())
	err := b.Run(context.Background(), BackfillConfig{Type: "bogus"})
	if err == nil {
		t.Fatal("want error for unknown type")
	}
}

func TestBackfiller_Equity_StoresCandles(t *testing.T) {
	db := openTestDB(t)
	seedEquityInstrument(t, db)
	blob := newFakeBlob()

	ts := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		candles: []provider.Candle{
			{Time: ts, Open: 100, High: 110, Low: 90, Close: 105, Volume: 500},
		},
	}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)

	b := NewBackfiller(fp, blob, db, testCfg())
	if err := b.Run(context.Background(), BackfillConfig{
		Type: "equity", Interval: "day", StartDate: start, EndDate: end,
	}); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Parquet file should exist
	key := pq.EquityDayKey("RELIANCE", "2024")
	if _, ok := blob.data[key]; !ok {
		t.Errorf("expected parquet at %s; keys present: %v", key, keys(blob.data))
	}

	// Checkpoint should be set to the end of the chunk (= end date, same year)
	last, found, err := db.GetLastDate("NSE", "RELIANCE", "day")
	if err != nil {
		t.Fatalf("GetLastDate: %v", err)
	}
	if !found {
		t.Fatal("checkpoint not set")
	}
	if !last.Equal(end) {
		t.Errorf("checkpoint: want %v, got %v", end, last)
	}
}

func TestBackfiller_Equity_CheckpointNotAdvancedOnProviderError(t *testing.T) {
	db := openTestDB(t)
	seedEquityInstrument(t, db)

	fp := &fakeProvider{histErr: errors.New("API error")}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)

	b := NewBackfiller(fp, newFakeBlob(), db, testCfg())
	// Error from provider is logged and skipped — Run itself succeeds
	_ = b.Run(context.Background(), BackfillConfig{
		Type: "equity", Interval: "day", StartDate: start, EndDate: end,
	})

	_, found, err := db.GetLastDate("NSE", "RELIANCE", "day")
	if err != nil {
		t.Fatalf("GetLastDate: %v", err)
	}
	if found {
		t.Error("checkpoint must not be set when all provider calls errored")
	}
}

func TestBackfiller_Equity_ResumesFromCheckpoint(t *testing.T) {
	db := openTestDB(t)
	seedEquityInstrument(t, db)

	// Set checkpoint to Jan 3
	checkpoint := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := db.SetLastDate("NSE", "RELIANCE", "day", checkpoint); err != nil {
		t.Fatalf("SetLastDate: %v", err)
	}

	ts := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		candles: []provider.Candle{{Time: ts, Open: 100, High: 110, Low: 90, Close: 105}},
	}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)

	b := NewBackfiller(fp, newFakeBlob(), db, testCfg())
	if err := b.Run(context.Background(), BackfillConfig{
		Type: "equity", Interval: "day", StartDate: start, EndDate: end,
	}); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if len(fp.historicalCalls) == 0 {
		t.Fatal("expected at least one historical call")
	}
	// startFrom = checkpoint + 1 = Jan 4
	wantFrom := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	if !fp.historicalCalls[0].from.Equal(wantFrom) {
		t.Errorf("historical called with from=%v, want %v", fp.historicalCalls[0].from, wantFrom)
	}
}

func TestBackfiller_Equity_SkipsWhenAlreadyComplete(t *testing.T) {
	db := openTestDB(t)
	seedEquityInstrument(t, db)

	end := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)
	if err := db.SetLastDate("NSE", "RELIANCE", "day", end); err != nil {
		t.Fatalf("SetLastDate: %v", err)
	}

	fp := &fakeProvider{}
	b := NewBackfiller(fp, newFakeBlob(), db, testCfg())
	if err := b.Run(context.Background(), BackfillConfig{
		Type: "equity", Interval: "day",
		StartDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   end,
	}); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if len(fp.historicalCalls) > 0 {
		t.Errorf("provider should not be called when checkpoint == end date; got %d calls", len(fp.historicalCalls))
	}
}

func TestBackfiller_Equity_FetchesInstrumentsFromProviderWhenDBEmpty(t *testing.T) {
	db := openTestDB(t)
	// DB has no instruments — backfiller must call provider.Instruments

	ts := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		instruments: map[string][]provider.Instrument{
			"NSE": {
				{Token: "99999", ExchangeToken: 2, TradingSymbol: "INFY",
					Exchange: "NSE", InstrumentType: "EQ", Segment: "NSE"},
			},
		},
		candles: []provider.Candle{{Time: ts, Open: 200, High: 220, Low: 195, Close: 210}},
	}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)

	b := NewBackfiller(fp, newFakeBlob(), db, testCfg())
	if err := b.Run(context.Background(), BackfillConfig{
		Type: "equity", Interval: "day", StartDate: start, EndDate: end,
	}); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Provider must have been called for candles (means instruments were fetched)
	if len(fp.historicalCalls) == 0 {
		t.Error("expected historical call after fetching instruments from provider")
	}
}

func TestOptionsExtractor_FilterByUnderlying_Integration(t *testing.T) {
	db := openTestDB(t)
	blob := newFakeBlob()

	// Seed NIFTY and BANKNIFTY options
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	err := db.UpsertInstruments(day, []pq.InstrumentRecord{
		{Token: 1, TradingSymbol: "NIFTY24JAN18000CE", Exchange: "NFO", InstrumentType: "CE", Segment: "NFO", Expiry: "2024-01-25"},
		{Token: 2, TradingSymbol: "BANKNIFTY24JAN45000CE", Exchange: "NFO", InstrumentType: "CE", Segment: "NFO", Expiry: "2024-01-25"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	ts := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		candles: []provider.Candle{{Time: ts, Open: 50, High: 60, Low: 45, Close: 55}},
	}

	cfg := testCfg()
	o := NewOptionsExtractor(fp, blob, db, cfg)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)

	if err := o.Backfill(context.Background(), "day", start, end); err != nil {
		t.Fatalf("options backfill: %v", err)
	}

	// Only NIFTY contracts should have been queried (1 call for NIFTY CE only)
	if len(fp.historicalCalls) != 1 {
		t.Errorf("want 1 historical call (only NIFTY CE), got %d", len(fp.historicalCalls))
	}

	// Checkpoint should be set for NIFTY
	_, found, err := db.GetOptionsLastDate("NIFTY", "day")
	if err != nil {
		t.Fatalf("GetOptionsLastDate: %v", err)
	}
	if !found {
		t.Error("NIFTY options checkpoint not set")
	}
}

// keys returns the key set of a map for error messages.
func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
