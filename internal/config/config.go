package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Extraction ExtractionConfig `yaml:"extraction"`
	Kite       KiteConfig       `yaml:"kite"`
	Cron       CronConfig       `yaml:"cron"`
	SQLitePath string           // populated from SQLITE_PATH env var
	R2Bucket   string           // populated from R2_BUCKET env var
}

type ExtractionConfig struct {
	Equity  AssetConfig   `yaml:"equity"`
	Futures AssetConfig   `yaml:"futures"`
	Options OptionsConfig `yaml:"options"`
}

type AssetConfig struct {
	Disabled       bool              `yaml:"disabled"`        // skip this asset type in incremental runs and scheduled backfill
	Exchanges      []string          `yaml:"exchanges"`
	Intervals      []string          `yaml:"intervals"`
	BackfillFrom   map[string]string `yaml:"backfill_from"`
	FnOOnly        bool              `yaml:"fno_only"`        // limit EQ to stocks with active F&O contracts
	IncludeIndices bool              `yaml:"include_indices"` // also extract NSE index instruments
	Indices        []string          `yaml:"indices"`         // specific indices to include (empty = all); accepts "NIFTY 50" or "NIFTY-50"
}

type OptionsConfig struct {
	Disabled     bool              `yaml:"disabled"`        // skip options in incremental runs and scheduled backfill
	Exchanges    []string          `yaml:"exchanges"`
	Underlyings  []string          `yaml:"underlyings"`
	Intervals    []string          `yaml:"intervals"`
	BackfillFrom map[string]string `yaml:"backfill_from"`
}

type KiteConfig struct {
	RateLimitRPS int            `yaml:"rate_limit_rps"`
	ChunkDays    map[string]int `yaml:"chunk_days"`
}

type CronConfig struct {
	Schedule         string `yaml:"schedule"`
	BackfillSchedule string `yaml:"backfill_schedule"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Single source of truth: env vars (must match litestream.yml and start.sh)
	cfg.SQLitePath = os.Getenv("SQLITE_PATH")
	if cfg.SQLitePath == "" {
		cfg.SQLitePath = "./data/stock.db" // local dev default
	}
	cfg.R2Bucket = os.Getenv("R2_BUCKET")

	// Validation
	if cfg.R2Bucket == "" {
		return nil, fmt.Errorf("R2_BUCKET env var is required")
	}
	if cfg.Kite.RateLimitRPS == 0 {
		return nil, fmt.Errorf("kite.rate_limit_rps is required")
	}
	if len(cfg.Extraction.Equity.Exchanges) == 0 {
		return nil, fmt.Errorf("extraction.equity.exchanges must not be empty")
	}
	if len(cfg.Extraction.Equity.Intervals) == 0 {
		return nil, fmt.Errorf("extraction.equity.intervals must not be empty")
	}
	if len(cfg.Extraction.Futures.Exchanges) == 0 {
		return nil, fmt.Errorf("extraction.futures.exchanges must not be empty")
	}
	if len(cfg.Extraction.Futures.Intervals) == 0 {
		return nil, fmt.Errorf("extraction.futures.intervals must not be empty")
	}
	if len(cfg.Extraction.Options.Exchanges) == 0 {
		return nil, fmt.Errorf("extraction.options.exchanges must not be empty")
	}
	if len(cfg.Extraction.Options.Underlyings) == 0 {
		return nil, fmt.Errorf("extraction.options.underlyings must not be empty")
	}
	if len(cfg.Extraction.Options.Intervals) == 0 {
		return nil, fmt.Errorf("extraction.options.intervals must not be empty")
	}

	return &cfg, nil
}
