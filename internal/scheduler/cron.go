package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/prabhatrastogik/stock-data-extract/internal/config"
	"github.com/prabhatrastogik/stock-data-extract/internal/extractor"
)

type Scheduler struct {
	cron        *cron.Cron
	incremental *extractor.IncrementalExtractor
	backfiller  *extractor.Backfiller
	cfg         *config.Config
	cancel      context.CancelFunc
}

func New(inc *extractor.IncrementalExtractor, bf *extractor.Backfiller, cfg *config.Config) *Scheduler {
	return &Scheduler{
		cron:        cron.New(),
		incremental: inc,
		backfiller:  bf,
		cfg:         cfg,
	}
}

func (s *Scheduler) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	if _, err := s.cron.AddFunc(s.cfg.Cron.Schedule, func() { s.runIncremental(ctx) }); err != nil {
		cancel()
		return err
	}
	if s.cfg.Cron.BackfillSchedule != "" {
		if _, err := s.cron.AddFunc(s.cfg.Cron.BackfillSchedule, func() { s.runWeeklyBackfill(ctx) }); err != nil {
			cancel()
			return err
		}
	}
	s.cron.Start()
	return nil
}

func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	// cron.Stop() stops scheduling new jobs and returns a context that resolves
	// once all currently running jobs have returned.
	<-s.cron.Stop().Done()
}

func (s *Scheduler) runIncremental(ctx context.Context) {
	log.Println("[scheduler] starting incremental extraction")
	if err := s.incremental.Run(ctx); err != nil {
		log.Printf("[scheduler] incremental failed: %v", err)
	} else {
		log.Println("[scheduler] incremental completed")
	}
}

// lastTradingDay returns the most recent weekday before today (skips Sat/Sun).
func lastTradingDay(today time.Time) time.Time {
	yesterday := today.AddDate(0, 0, -1)
	switch yesterday.Weekday() {
	case time.Saturday:
		return yesterday.AddDate(0, 0, -1)
	case time.Sunday:
		return yesterday.AddDate(0, 0, -2)
	}
	return yesterday
}

func (s *Scheduler) runWeeklyBackfill(ctx context.Context) {
	log.Println("[scheduler] starting weekly catch-up backfill")
	today := time.Now().UTC().Truncate(24 * time.Hour)
	end := lastTradingDay(today)

	cfg := s.cfg.Extraction
	type job struct {
		assetType string
		intervals []string
		startDates map[string]string
	}
	jobs := []job{
		{"equity", cfg.Equity.Intervals, cfg.Equity.BackfillFrom},
		{"futures", cfg.Futures.Intervals, cfg.Futures.BackfillFrom},
		{"options", cfg.Options.Intervals, cfg.Options.BackfillFrom},
	}

	for _, j := range jobs {
		for _, interval := range j.intervals {
			startStr, ok := j.startDates[interval]
			if !ok {
				continue
			}
			start, err := time.Parse("2006-01-02", startStr)
			if err != nil {
				log.Printf("[scheduler] backfill: bad start date %q: %v", startStr, err)
				continue
			}
			bcfg := extractor.BackfillConfig{
				Type:      j.assetType,
				Interval:  interval,
				StartDate: start,
				EndDate:   end,
			}
			log.Printf("[scheduler] backfill: type=%s interval=%s", j.assetType, interval)
			if err := s.backfiller.Run(ctx, bcfg); err != nil {
				log.Printf("[scheduler] backfill %s/%s failed: %v", j.assetType, interval, err)
			}
		}
	}

	log.Println("[scheduler] weekly catch-up backfill complete")
}
