package extractor

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/prabhatrastogik/stock-data-extract/internal/config"
	"github.com/prabhatrastogik/stock-data-extract/internal/provider"
	pq "github.com/prabhatrastogik/stock-data-extract/internal/storage/parquet"
	r2client "github.com/prabhatrastogik/stock-data-extract/internal/storage/r2"
	"github.com/prabhatrastogik/stock-data-extract/internal/storage/sqlite"
)

type OptionsExtractor struct {
	provider provider.Provider
	r2       r2client.BlobStore
	db       *sqlite.DB
	cfg      *config.Config
}

func NewOptionsExtractor(p provider.Provider, r2 r2client.BlobStore, db *sqlite.DB, cfg *config.Config) *OptionsExtractor {
	return &OptionsExtractor{provider: p, r2: r2, db: db, cfg: cfg}
}

func (o *OptionsExtractor) Backfill(ctx context.Context, interval string, startDate, endDate time.Time) error {
	for _, underlying := range o.cfg.Extraction.Options.Underlyings {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.extractUnderlying(ctx, underlying, interval, startDate, endDate); err != nil {
			log.Printf("[options] %s %s: %v (continuing)", underlying, interval, err)
		}
	}
	return nil
}

func (o *OptionsExtractor) RunIncremental(ctx context.Context, interval string, yesterday time.Time) error {
	for _, underlying := range o.cfg.Extraction.Options.Underlyings {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.extractUnderlying(ctx, underlying, interval, yesterday, yesterday); err != nil {
			log.Printf("[options] incremental %s %s: %v (continuing)", underlying, interval, err)
		}
	}
	return nil
}

func (o *OptionsExtractor) extractUnderlying(ctx context.Context, underlying, interval string, startDate, endDate time.Time) error {
	startFrom := startDate
	if last, found, err := o.db.GetOptionsLastDate(underlying, interval); err != nil {
		return err
	} else if found {
		startFrom = last.AddDate(0, 0, 1)
	}

	if startFrom.After(endDate) {
		return nil
	}

	// Load CE and PE contracts for this underlying across all configured exchanges.
	var ceInsts, peInsts []pq.InstrumentRecord
	for _, exchange := range o.cfg.Extraction.Options.Exchanges {
		ce, err := o.db.LatestInstruments(exchange, "CE")
		if err != nil {
			return fmt.Errorf("load CE instruments %s: %w", exchange, err)
		}
		pe, err := o.db.LatestInstruments(exchange, "PE")
		if err != nil {
			return fmt.Errorf("load PE instruments %s: %w", exchange, err)
		}
		ceInsts = append(ceInsts, ce...)
		peInsts = append(peInsts, pe...)
	}

	contracts := filterByUnderlying(underlying, append(ceInsts, peInsts...))
	if len(contracts) == 0 {
		log.Printf("[options] no contracts found for %s", underlying)
		return nil
	}

	log.Printf("[options] %s %s: %d contracts, %s → %s", underlying, interval, len(contracts), startFrom.Format("2006-01-02"), endDate.Format("2006-01-02"))

	chunkDays := o.cfg.Kite.ChunkDays[interval]
	if chunkDays == 0 {
		chunkDays = 60
	}

	chunks := chunkDateRange(startFrom, endDate, chunkDays)

	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}

		allOK := true
		subChunks := splitByPeriod(chunk[0], chunk[1], interval)
		for _, sc := range subChunks {
			// Accumulate all records for this period across all contracts
			keyRecords := make(map[string][]pq.OptionCandleRecord)

			for _, contract := range contracts {
				if err := ctx.Err(); err != nil {
					return err
				}
				token := fmt.Sprintf("%d", contract.Token)
				candles, err := o.provider.Historical(ctx, token, interval, sc[0], sc[1], false, true)
				if err != nil {
					if isAuthError(err) {
						return fmt.Errorf("access token invalid or expired — run token-refresh: %w", err)
					}
					log.Printf("[options] %s %s token %s: %v (skipping)", underlying, interval, token, err)
					allOK = false
					continue
				}
				if len(candles) == 0 {
					continue
				}

				expiryStr := contract.Expiry
				strike := contract.Strike
				optType := contract.InstrumentType

				for _, c := range candles {
					rec := pq.OptionCandleRecord{
						Timestamp:  c.Time.UnixMicro(),
						Expiry:     expiryStr,
						Strike:     strike,
						OptionType: optType,
						Open:       c.Open,
						High:       c.High,
						Low:        c.Low,
						Close:      c.Close,
						Volume:     c.Volume,
						OI:         c.OI,
					}
					key := optionsKey(underlying, sc[0], interval)
					keyRecords[key] = append(keyRecords[key], rec)
				}
			}

			for key, records := range keyRecords {
				if err := pq.AppendOptionCandles(ctx, o.r2, key, records); err != nil {
					return fmt.Errorf("append option candles %s: %w", key, err)
				}
			}
		}

		// Only advance checkpoint if every contract fetch succeeded.
		// On restart the chunk will be re-fetched; Parquet writes are idempotent.
		if allOK {
			if err := o.db.SetOptionsLastDate(underlying, interval, chunk[1]); err != nil {
				return err
			}
		}
	}

	log.Printf("[options] done: %s %s", underlying, interval)
	return nil
}

// filterByUnderlying selects contracts whose TradingSymbol starts with the
// underlying name (e.g. "NIFTY25JULFUT", "RELIANCE24DEC3000CE"). This is
// correct for both index and stock options; using r.Name would fail for stock
// options where Kite's name field is the full company name (e.g.
// "RELIANCE INDUSTRIES"), not the symbol.
func filterByUnderlying(underlying string, records []pq.InstrumentRecord) []pq.InstrumentRecord {
	var out []pq.InstrumentRecord
	for _, r := range records {
		if strings.HasPrefix(r.TradingSymbol, underlying) {
			out = append(out, r)
		}
	}
	return out
}

func optionsKey(underlying string, t time.Time, interval string) string {
	if interval == "day" {
		return pq.OptionsDayKey(underlying, fmt.Sprintf("%d", t.Year()))
	}
	return pq.Options15MinKey(underlying, t.Format("2006-01"))
}
