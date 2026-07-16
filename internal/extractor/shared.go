package extractor

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/prabhatrastogik/stock-data-extract/internal/config"
	"github.com/prabhatrastogik/stock-data-extract/internal/provider"
	pq "github.com/prabhatrastogik/stock-data-extract/internal/storage/parquet"
	"github.com/prabhatrastogik/stock-data-extract/internal/storage/sqlite"
)

// AutoRefreshConfig holds credentials needed for automated token refresh.
// Both IncrementalExtractor and Backfiller use it.
type AutoRefreshConfig struct {
	APIKey     string
	APISecret  string
	UserID     string
	Password   string
	TOTPSecret string
}

// endOfDay returns the end of the given day for the Kite API.
// For daily candles, from=to=midnight is fine (date-based comparison).
// For intraday (15min), from=to=midnight is a zero-length window before market
// open and returns nothing — use end-of-day so the full trading session is covered.
func endOfDay(day time.Time, interval string) time.Time {
	if interval == "day" {
		return day
	}
	return day.Add(24*time.Hour - time.Second)
}

// LastTradingDay returns yesterday, skipping weekends (Saturday→Friday, Sunday→Friday).
// Exported so scheduler and cmd can use it without duplicating the logic.
func LastTradingDay(today time.Time) time.Time {
	yesterday := today.AddDate(0, 0, -1)
	switch yesterday.Weekday() {
	case time.Saturday:
		return yesterday.AddDate(0, 0, -1)
	case time.Sunday:
		return yesterday.AddDate(0, 0, -2)
	}
	return yesterday
}

// isAuthError returns true when the Kite API rejects a call due to an invalid or
// expired access token. Auth errors are non-retriable and abort the entire run.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "access_token") || strings.Contains(s, "TokenException") || strings.Contains(s, "403")
}

func equityKey(symbol string, t time.Time, interval string) string {
	if interval == "day" {
		return pq.EquityDayKey(symbol, fmt.Sprintf("%d", t.Year()))
	}
	return pq.Equity15MinKey(symbol, t.Format("2006-01"))
}

func futuresKey(symbol string, t time.Time, interval string) string {
	if interval == "day" {
		return pq.FuturesDayKey(symbol, fmt.Sprintf("%d", t.Year()))
	}
	return pq.Futures15MinKey(symbol, t.Format("2006-01"))
}

func candlesToRecords(candles []provider.Candle) []pq.CandleRecord {
	out := make([]pq.CandleRecord, len(candles))
	for i, c := range candles {
		out[i] = pq.CandleRecord{
			Timestamp: c.Time.UnixMicro(),
			Open:      c.Open,
			High:      c.High,
			Low:       c.Low,
			Close:     c.Close,
			Volume:    c.Volume,
			OI:        c.OI,
		}
	}
	return out
}

// sanitizeSymbol replaces characters that are unsafe in R2 object keys and SQLite
// state keys. NSE index tradingsymbols contain spaces (e.g. "NIFTY 50"); these are
// normalised to hyphens so keys are well-formed without percent-encoding.
func sanitizeSymbol(s string) string {
	return strings.ReplaceAll(s, " ", "-")
}

// equityInstrumentsFromDB returns the equity instruments to extract for exchange,
// applying fno_only and include_indices filters from config. Instruments must
// already be in the DB before calling this (backfill: call loadOrFetchInstruments
// first; incremental: fetchAndStoreInstruments has already run).
func equityInstrumentsFromDB(db *sqlite.DB, cfg *config.Config, exchange string) ([]struct {
	TradingSymbol string
	Token         string
}, error) {
	var recs []pq.InstrumentRecord
	var err error

	if cfg.Extraction.Equity.FnOOnly {
		recs, err = db.FnOEquityInstruments(exchange, cfg.Extraction.Futures.Exchanges)
	} else {
		recs, err = db.LatestInstruments(exchange, "EQ")
	}
	if err != nil {
		return nil, err
	}

	if cfg.Extraction.Equity.IncludeIndices {
		// Kite stores NSE index instruments with instrument_type="EQ" and segment="INDICES",
		// so we must filter by segment, not instrument_type.
		idxRecs, err := db.LatestInstrumentsBySegment(exchange, "INDICES")
		if err != nil {
			return nil, err
		}
		if len(cfg.Extraction.Equity.Indices) > 0 {
			// Build a set of allowed symbols (normalise both sides: spaces → hyphens).
			allowed := make(map[string]struct{}, len(cfg.Extraction.Equity.Indices))
			for _, s := range cfg.Extraction.Equity.Indices {
				allowed[sanitizeSymbol(s)] = struct{}{}
			}
			filtered := idxRecs[:0]
			for _, r := range idxRecs {
				if _, ok := allowed[sanitizeSymbol(r.TradingSymbol)]; ok {
					filtered = append(filtered, r)
				}
			}
			idxRecs = filtered
		}
		recs = append(recs, idxRecs...)
	}

	out := make([]struct {
		TradingSymbol string
		Token         string
	}, len(recs))
	for i, r := range recs {
		out[i].TradingSymbol = sanitizeSymbol(r.TradingSymbol)
		out[i].Token = fmt.Sprintf("%d", r.Token)
	}
	return out, nil
}

func instrumentsToParquet(insts []provider.Instrument) []pq.InstrumentRecord {
	out := make([]pq.InstrumentRecord, len(insts))
	for i, inst := range insts {
		expiry := ""
		if !inst.Expiry.IsZero() {
			expiry = inst.Expiry.Format("2006-01-02")
		}
		token, err := strconv.ParseInt(inst.Token, 10, 64)
		if err != nil {
			log.Printf("[instrumentsToParquet] invalid token %q: %v", inst.Token, err)
		}
		out[i] = pq.InstrumentRecord{
			Token:          token,
			ExchangeToken:  int64(inst.ExchangeToken),
			TradingSymbol:  inst.TradingSymbol,
			Name:           inst.Name,
			Exchange:       inst.Exchange,
			InstrumentType: inst.InstrumentType,
			Segment:        inst.Segment,
			Expiry:         expiry,
			Strike:         inst.Strike,
			LotSize:        int64(inst.LotSize),
			TickSize:       inst.TickSize,
		}
	}
	return out
}
