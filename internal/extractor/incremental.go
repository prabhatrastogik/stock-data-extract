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

type IncrementalExtractor struct {
	provider         provider.RefreshableProvider
	r2               r2client.BlobStore
	db               *sqlite.DB
	cfg              *config.Config
	autoRefreshCfg   *AutoRefreshConfig
	optionsExtractor *OptionsExtractor
}

func NewIncrementalExtractor(
	p provider.RefreshableProvider,
	r2 r2client.BlobStore,
	db *sqlite.DB,
	cfg *config.Config,
	autoRefreshCfg *AutoRefreshConfig,
) *IncrementalExtractor {
	return &IncrementalExtractor{
		provider:         p,
		r2:               r2,
		db:               db,
		cfg:              cfg,
		autoRefreshCfg:   autoRefreshCfg,
		optionsExtractor: NewOptionsExtractor(p, r2, db, cfg),
	}
}

// Run performs one full incremental extraction pass.
func (e *IncrementalExtractor) Run(ctx context.Context) error {
	log.Println("[incremental] starting run")

	// Validate token, auto-refresh if expired
	if err := e.provider.ValidateToken(ctx); err != nil {
		log.Printf("[incremental] token validation failed: %v", err)
		if e.autoRefreshCfg == nil {
			return fmt.Errorf("access token invalid and auto-refresh not configured — run token-refresh: %w", err)
		}
		log.Println("[incremental] attempting auto token refresh...")
		newToken, refreshErr := kiteprovider.AutoLogin(
			e.autoRefreshCfg.APIKey,
			e.autoRefreshCfg.APISecret,
			e.autoRefreshCfg.UserID,
			e.autoRefreshCfg.Password,
			e.autoRefreshCfg.TOTPSecret,
		)
		if refreshErr != nil {
			return fmt.Errorf("token expired and auto-refresh failed: %w", refreshErr)
		}
		e.provider.SetAccessToken(newToken)
		_ = os.Setenv("KITE_ACCESS_TOKEN", newToken)
		log.Println("[incremental] token refreshed successfully")
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	yesterday := LastTradingDay(today)

	// Step 1: Fetch and store instruments snapshot
	if err := e.fetchAndStoreInstruments(ctx, today); err != nil {
		return fmt.Errorf("instruments snapshot: %w", err)
	}

	// Step 2: Equity
	if !e.cfg.Extraction.Equity.Disabled {
		if err := e.runEquityIncremental(ctx, yesterday); err != nil {
			log.Printf("[incremental] equity error: %v", err)
		}
	} else {
		log.Println("[incremental] equity disabled — skipping")
	}

	// Step 3: Futures
	if !e.cfg.Extraction.Futures.Disabled {
		if err := e.runFuturesIncremental(ctx, yesterday); err != nil {
			log.Printf("[incremental] futures error: %v", err)
		}
	} else {
		log.Println("[incremental] futures disabled — skipping")
	}

	// Step 4: Options
	if !e.cfg.Extraction.Options.Disabled {
		for _, interval := range e.cfg.Extraction.Options.Intervals {
			if err := e.optionsExtractor.RunIncremental(ctx, interval, yesterday); err != nil {
				log.Printf("[incremental] options %s error: %v", interval, err)
			}
		}
	} else {
		log.Println("[incremental] options disabled — skipping")
	}

	log.Println("[incremental] run complete")
	return nil
}

func (e *IncrementalExtractor) fetchAndStoreInstruments(ctx context.Context, today time.Time) error {
	log.Println("[incremental] fetching instruments snapshot")

	var allInsts []pq.InstrumentRecord

	// Collect the unique set of exchanges across equity, futures, and options config.
	seen := make(map[string]bool)
	var exchanges []string
	all := append(append(e.cfg.Extraction.Equity.Exchanges, e.cfg.Extraction.Futures.Exchanges...), e.cfg.Extraction.Options.Exchanges...)
	for _, ex := range all {
		if !seen[ex] {
			seen[ex] = true
			exchanges = append(exchanges, ex)
		}
	}

	for _, exchange := range exchanges {
		insts, err := e.provider.Instruments(ctx, exchange)
		if err != nil {
			return fmt.Errorf("fetch %s instruments: %w", exchange, err)
		}
		allInsts = append(allInsts, instrumentsToParquet(insts)...)
	}

	if err := e.db.UpsertInstruments(today, allInsts); err != nil {
		return fmt.Errorf("upsert instruments: %w", err)
	}

	key := pq.InstrumentsSnapshotKey(today)
	if err := pq.WriteInstruments(ctx, e.r2, key, allInsts); err != nil {
		return fmt.Errorf("write instruments snapshot: %w", err)
	}

	log.Printf("[incremental] instruments snapshot written: %d records", len(allInsts))
	return nil
}

func (e *IncrementalExtractor) runEquityIncremental(ctx context.Context, day time.Time) error {
	for _, exchange := range e.cfg.Extraction.Equity.Exchanges {
		// Instruments are already in DB (fetchAndStoreInstruments ran first).
		instruments, err := equityInstrumentsFromDB(e.db, e.cfg, exchange)
		if err != nil {
			return err
		}

		log.Printf("[incremental] equity %s: %d symbols for %s", exchange, len(instruments), day.Format("2006-01-02"))

		for _, inst := range instruments {
			if err := ctx.Err(); err != nil {
				return err
			}

			for _, interval := range e.cfg.Extraction.Equity.Intervals {
				to := endOfDay(day, interval)
				candles, err := e.provider.Historical(ctx, inst.Token, interval, day, to, false, false)
				if err != nil {
					if isAuthError(err) {
						return fmt.Errorf("access token invalid or expired — run token-refresh: %w", err)
					}
					log.Printf("[incremental] equity %s %s: %v (skipping)", inst.TradingSymbol, interval, err)
					continue
				}
				if len(candles) == 0 {
					continue
				}

				key := equityKey(inst.TradingSymbol, day, interval)
				if err := pq.AppendCandles(ctx, e.r2, key, candlesToRecords(candles)); err != nil {
					log.Printf("[incremental] equity %s write: %v", inst.TradingSymbol, err)
					continue
				}

				if err := e.db.SetLastDate(exchange, inst.TradingSymbol, interval, day); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (e *IncrementalExtractor) runFuturesIncremental(ctx context.Context, day time.Time) error {
	for _, exchange := range e.cfg.Extraction.Futures.Exchanges {
		instruments, err := e.db.LatestInstruments(exchange, "FUT")
		if err != nil {
			return err
		}

		log.Printf("[incremental] futures %s: %d contracts for %s", exchange, len(instruments), day.Format("2006-01-02"))

		for _, inst := range instruments {
			if err := ctx.Err(); err != nil {
				return err
			}

			token := fmt.Sprintf("%d", inst.Token)
			for _, interval := range e.cfg.Extraction.Futures.Intervals {
				to := endOfDay(day, interval)
				candles, err := e.provider.Historical(ctx, token, interval, day, to, false, true)
				if err != nil {
					if isAuthError(err) {
						return fmt.Errorf("access token invalid or expired — run token-refresh: %w", err)
					}
					log.Printf("[incremental] futures %s %s: %v (skipping)", inst.TradingSymbol, interval, err)
					continue
				}
				if len(candles) == 0 {
					continue
				}

				key := futuresKey(inst.TradingSymbol, day, interval)
				if err := pq.AppendCandles(ctx, e.r2, key, candlesToRecords(candles)); err != nil {
					log.Printf("[incremental] futures %s write: %v", inst.TradingSymbol, err)
					continue
				}

				if err := e.db.SetLastDate(exchange, inst.TradingSymbol, interval, day); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
