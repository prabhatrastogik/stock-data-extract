package extractor

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/prabhatrastogik/stock-data-extract/internal/config"
	"github.com/prabhatrastogik/stock-data-extract/internal/provider"
	kiteprovider "github.com/prabhatrastogik/stock-data-extract/internal/provider/kite"
	pq "github.com/prabhatrastogik/stock-data-extract/internal/storage/parquet"
	r2client "github.com/prabhatrastogik/stock-data-extract/internal/storage/r2"
	"github.com/prabhatrastogik/stock-data-extract/internal/storage/sqlite"
)

type BackfillConfig struct {
	Type      string // "equity", "futures", "options"
	Interval  string // "day" or "15min"
	StartDate time.Time
	EndDate   time.Time
}

type Backfiller struct {
	provider       provider.RefreshableProvider
	r2             r2client.BlobStore
	db             *sqlite.DB
	cfg            *config.Config
	autoRefreshCfg *AutoRefreshConfig
}

func NewBackfiller(p provider.RefreshableProvider, r2 r2client.BlobStore, db *sqlite.DB, cfg *config.Config, autoRefreshCfg *AutoRefreshConfig) *Backfiller {
	return &Backfiller{provider: p, r2: r2, db: db, cfg: cfg, autoRefreshCfg: autoRefreshCfg}
}

func (b *Backfiller) Run(ctx context.Context, cfg BackfillConfig) error {
	// Validate token before starting; auto-refresh if credentials are configured.
	if err := b.provider.ValidateToken(ctx); err != nil {
		log.Printf("[backfill] token validation failed: %v", err)
		if b.autoRefreshCfg == nil {
			return fmt.Errorf("access token invalid and auto-refresh not configured — run token-refresh: %w", err)
		}
		log.Println("[backfill] attempting auto token refresh...")
		newToken, refreshErr := kiteprovider.AutoLogin(
			b.autoRefreshCfg.APIKey,
			b.autoRefreshCfg.APISecret,
			b.autoRefreshCfg.UserID,
			b.autoRefreshCfg.Password,
			b.autoRefreshCfg.TOTPSecret,
		)
		if refreshErr != nil {
			return fmt.Errorf("token expired and auto-refresh failed: %w", refreshErr)
		}
		b.provider.SetAccessToken(newToken)
		_ = os.Setenv("KITE_ACCESS_TOKEN", newToken)
		log.Println("[backfill] token refreshed successfully")
	}

	switch cfg.Type {
	case "equity":
		return b.runEquity(ctx, cfg)
	case "futures":
		return b.runFutures(ctx, cfg)
	case "options":
		optExt := NewOptionsExtractor(b.provider, b.r2, b.db, b.cfg)
		return optExt.Backfill(ctx, cfg.Interval, cfg.StartDate, cfg.EndDate)
	default:
		return fmt.Errorf("unknown backfill type %q", cfg.Type)
	}
}

func (b *Backfiller) runEquity(ctx context.Context, cfg BackfillConfig) error {
	chunkDays := b.cfg.Kite.ChunkDays[cfg.Interval]
	if chunkDays == 0 {
		chunkDays = 60
	}

	// fno_only cross-references NSE EQ against NFO FUT names, so NFO instruments
	// must be in the DB before the equity loop runs — even when only --type equity
	// was requested and runFutures has not run.
	if b.cfg.Extraction.Equity.FnOOnly {
		for _, futEx := range b.cfg.Extraction.Futures.Exchanges {
			if _, err := b.loadOrFetchInstruments(ctx, futEx, "FUT"); err != nil {
				return err
			}
		}
	}

	for _, exchange := range b.cfg.Extraction.Equity.Exchanges {
		// Ensure EQ instruments are in DB (fetches from Kite API if absent).
		if _, err := b.loadOrFetchInstruments(ctx, exchange, "EQ"); err != nil {
			return err
		}
		instruments, err := equityInstrumentsFromDB(b.db, b.cfg, exchange)
		if err != nil {
			return err
		}

		log.Printf("[backfill] equity %s %s: %d symbols", exchange, cfg.Interval, len(instruments))

		for _, inst := range instruments {
			if err := ctx.Err(); err != nil {
				return err
			}

			startFrom := cfg.StartDate
			if last, found, err := b.db.GetLastDate(exchange, inst.TradingSymbol, cfg.Interval); err != nil {
				return err
			} else if found {
				startFrom = last.AddDate(0, 0, 1)
			}

			if startFrom.After(cfg.EndDate) {
				continue
			}

			chunks := chunkDateRange(startFrom, cfg.EndDate, chunkDays)
			for _, chunk := range chunks {
				if err := ctx.Err(); err != nil {
					return err
				}

				allOK := true
				subChunks := splitByPeriod(chunk[0], chunk[1], cfg.Interval)
				for _, sc := range subChunks {
					candles, err := b.provider.Historical(ctx, inst.Token, cfg.Interval, sc[0], sc[1], false, false)
					if err != nil {
						if isAuthError(err) {
							return fmt.Errorf("access token invalid or expired — run token-refresh: %w", err)
						}
						log.Printf("[backfill] equity %s %s: %v (skipping)", inst.TradingSymbol, cfg.Interval, err)
						allOK = false
						continue
					}
					if len(candles) == 0 {
						continue
					}

					key := equityKey(inst.TradingSymbol, sc[0], cfg.Interval)
					records := candlesToRecords(candles)
					if err := pq.AppendCandles(ctx, b.r2, key, records); err != nil {
						return fmt.Errorf("append candles %s: %w", key, err)
					}
				}

				// Only advance the checkpoint if every sub-chunk succeeded.
				// On restart the chunk will be re-fetched; Parquet writes are idempotent.
				if allOK {
					if err := b.db.SetLastDate(exchange, inst.TradingSymbol, cfg.Interval, chunk[1]); err != nil {
						return err
					}
				}
			}

			log.Printf("[backfill] equity done: %s", inst.TradingSymbol)
		}
	}

	return nil
}

func (b *Backfiller) runFutures(ctx context.Context, cfg BackfillConfig) error {
	chunkDays := b.cfg.Kite.ChunkDays[cfg.Interval]
	if chunkDays == 0 {
		chunkDays = 60
	}

	for _, exchange := range b.cfg.Extraction.Futures.Exchanges {
		instruments, err := b.loadOrFetchInstruments(ctx, exchange, "FUT")
		if err != nil {
			return err
		}

		log.Printf("[backfill] futures %s %s: %d contracts", exchange, cfg.Interval, len(instruments))

		for _, inst := range instruments {
			if err := ctx.Err(); err != nil {
				return err
			}

			startFrom := cfg.StartDate
			if last, found, err := b.db.GetLastDate(exchange, inst.TradingSymbol, cfg.Interval); err != nil {
				return err
			} else if found {
				startFrom = last.AddDate(0, 0, 1)
			}

			if startFrom.After(cfg.EndDate) {
				continue
			}

			chunks := chunkDateRange(startFrom, cfg.EndDate, chunkDays)
			for _, chunk := range chunks {
				if err := ctx.Err(); err != nil {
					return err
				}

				allOK := true
				subChunks := splitByPeriod(chunk[0], chunk[1], cfg.Interval)
				for _, sc := range subChunks {
					candles, err := b.provider.Historical(ctx, inst.Token, cfg.Interval, sc[0], sc[1], false, true)
					if err != nil {
						if isAuthError(err) {
							return fmt.Errorf("access token invalid or expired — run token-refresh: %w", err)
						}
						log.Printf("[backfill] futures %s %s: %v (skipping)", inst.TradingSymbol, cfg.Interval, err)
						allOK = false
						continue
					}
					if len(candles) == 0 {
						continue
					}

					key := futuresKey(inst.TradingSymbol, sc[0], cfg.Interval)
					records := candlesToRecords(candles)
					if err := pq.AppendCandles(ctx, b.r2, key, records); err != nil {
						return fmt.Errorf("append futures candles %s: %w", key, err)
					}
				}

				if allOK {
					if err := b.db.SetLastDate(exchange, inst.TradingSymbol, cfg.Interval, chunk[1]); err != nil {
						return err
					}
				}
			}

			log.Printf("[backfill] futures done: %s", inst.TradingSymbol)
		}
	}

	return nil
}

func (b *Backfiller) loadOrFetchInstruments(ctx context.Context, exchange, instType string) ([]struct {
	TradingSymbol string
	Token         string
}, error) {
	dbRecords, err := b.db.LatestInstruments(exchange, instType)
	if err != nil {
		return nil, err
	}

	if len(dbRecords) == 0 {
		log.Printf("[backfill] no instruments in DB for %s/%s, fetching from Kite...", exchange, instType)
		provInsts, err := b.provider.Instruments(ctx, exchange)
		if err != nil {
			return nil, fmt.Errorf("fetch instruments %s: %w", exchange, err)
		}
		today := time.Now().UTC().Truncate(24 * time.Hour)
		parquetRecs := instrumentsToParquet(provInsts)
		if err := b.db.UpsertInstruments(today, parquetRecs); err != nil {
			return nil, err
		}
		dbRecords, err = b.db.LatestInstruments(exchange, instType)
		if err != nil {
			return nil, err
		}
	}

	out := make([]struct {
		TradingSymbol string
		Token         string
	}, len(dbRecords))
	for i, r := range dbRecords {
		out[i].TradingSymbol = r.TradingSymbol
		out[i].Token = fmt.Sprintf("%d", r.Token)
	}
	return out, nil
}

// chunkDateRange splits [from, to] into slices of at most maxDays each.
func chunkDateRange(from, to time.Time, maxDays int) [][2]time.Time {
	var chunks [][2]time.Time
	for cursor := from; !cursor.After(to); {
		end := cursor.AddDate(0, 0, maxDays-1)
		if end.After(to) {
			end = to
		}
		chunks = append(chunks, [2]time.Time{cursor, end})
		cursor = end.AddDate(0, 0, 1)
	}
	return chunks
}

// splitByPeriod subdivides a date range so each sub-range falls within one year (daily)
// or one month (15min). This ensures each AppendCandles call targets the correct key.
func splitByPeriod(from, to time.Time, interval string) [][2]time.Time {
	if interval == "day" {
		return splitByYear(from, to)
	}
	return splitByMonth(from, to)
}

func splitByYear(from, to time.Time) [][2]time.Time {
	var out [][2]time.Time
	for cursor := from; !cursor.After(to); {
		yearEnd := time.Date(cursor.Year(), 12, 31, 0, 0, 0, 0, cursor.Location())
		if yearEnd.After(to) {
			yearEnd = to
		}
		out = append(out, [2]time.Time{cursor, yearEnd})
		cursor = time.Date(cursor.Year()+1, 1, 1, 0, 0, 0, 0, cursor.Location())
	}
	return out
}

func splitByMonth(from, to time.Time) [][2]time.Time {
	var out [][2]time.Time
	for cursor := from; !cursor.After(to); {
		// last day of current month
		monthEnd := time.Date(cursor.Year(), cursor.Month()+1, 0, 0, 0, 0, 0, cursor.Location())
		if monthEnd.After(to) {
			monthEnd = to
		}
		out = append(out, [2]time.Time{cursor, monthEnd})
		cursor = time.Date(cursor.Year(), cursor.Month()+1, 1, 0, 0, 0, 0, cursor.Location())
	}
	return out
}
