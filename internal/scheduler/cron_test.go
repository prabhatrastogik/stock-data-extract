package scheduler

import (
	"testing"

	"github.com/prabhatrastogik/stock-data-extract/internal/config"
)

func schedulerCfg(schedule, backfillSchedule string) *config.Config {
	return &config.Config{
		Extraction: config.ExtractionConfig{
			Equity: config.AssetConfig{
				Intervals:    []string{"day"},
				BackfillFrom: map[string]string{"day": "2024-01-01"},
			},
			Futures: config.AssetConfig{
				Intervals:    []string{"day"},
				BackfillFrom: map[string]string{"day": "2024-01-01"},
			},
			Options: config.OptionsConfig{
				Intervals:    []string{"day"},
				BackfillFrom: map[string]string{"day": "2024-01-01"},
			},
		},
		Kite: config.KiteConfig{
			ChunkDays: map[string]int{"day": 365},
		},
		Cron: config.CronConfig{
			Schedule:         schedule,
			BackfillSchedule: backfillSchedule,
		},
	}
}

func TestScheduler_InvalidIncrementalSchedule(t *testing.T) {
	s := New(nil, nil, schedulerCfg("not-a-cron", ""))
	if err := s.Start(); err == nil {
		t.Fatal("Start must error for invalid incremental cron expression")
	}
}

func TestScheduler_InvalidBackfillSchedule(t *testing.T) {
	s := New(nil, nil, schedulerCfg("0 3 * * 2-6", "bad-cron"))
	if err := s.Start(); err == nil {
		defer s.Stop()
		t.Fatal("Start must error for invalid backfill cron expression")
	}
}

func TestScheduler_ValidIncrementalOnly(t *testing.T) {
	s := New(nil, nil, schedulerCfg("0 3 * * 2-6", ""))
	if err := s.Start(); err != nil {
		t.Fatalf("Start with valid incremental schedule: %v", err)
	}
	s.Stop()
}

func TestScheduler_BothSchedulesValid(t *testing.T) {
	s := New(nil, nil, schedulerCfg("0 3 * * 2-6", "0 5 * * 6"))
	if err := s.Start(); err != nil {
		t.Fatalf("Start with both schedules: %v", err)
	}
	s.Stop()
}

func TestScheduler_StopIdempotent(t *testing.T) {
	s := New(nil, nil, schedulerCfg("0 3 * * 2-6", ""))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Stop()
	s.Stop() // second Stop must not panic
}
