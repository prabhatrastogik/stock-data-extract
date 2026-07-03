package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prabhatrastogik/stock-data-extract/internal/config"
	"github.com/prabhatrastogik/stock-data-extract/internal/extractor"
	kiteprovider "github.com/prabhatrastogik/stock-data-extract/internal/provider/kite"
	"github.com/prabhatrastogik/stock-data-extract/internal/scheduler"
	r2client "github.com/prabhatrastogik/stock-data-extract/internal/storage/r2"
	"github.com/prabhatrastogik/stock-data-extract/internal/storage/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: stock-data-extract <command>")
		fmt.Fprintln(os.Stderr, "commands: run, backfill, token-refresh")
		os.Exit(1)
	}

	cfg, err := config.Load(envOrDefault("CONFIG_PATH", "config.yaml"))
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := sqlite.Open(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	defer db.Close()

	r2 := r2client.New(
		mustEnv("R2_ACCOUNT_ID"),
		mustEnv("R2_ACCESS_KEY_ID"),
		mustEnv("R2_SECRET_ACCESS_KEY"),
		cfg.R2Bucket,
	)

	kiteProvider := kiteprovider.New(
		mustEnv("KITE_API_KEY"),
		os.Getenv("KITE_ACCESS_TOKEN"), // optional if auto-refresh credentials are set
		cfg.Kite.RateLimitRPS,
	)

	autoRefreshCfg := buildAutoRefreshConfig()

	switch os.Args[1] {
	case "run":
		runScheduler(cfg, kiteProvider, r2, db, autoRefreshCfg)

	case "backfill":
		runBackfill(cfg, kiteProvider, r2, db, os.Args[2:])

	case "token-refresh":
		runTokenRefresh()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runScheduler(
	cfg *config.Config,
	kiteProvider *kiteprovider.KiteProvider,
	r2 *r2client.Client,
	db *sqlite.DB,
	autoRefreshCfg *extractor.AutoRefreshConfig,
) {
	if cfg.Cron.Schedule == "" {
		log.Fatal("cron.schedule is required in config.yaml for the run command")
	}

	inc := extractor.NewIncrementalExtractor(kiteProvider, r2, db, cfg, autoRefreshCfg)
	bf := extractor.NewBackfiller(kiteProvider, r2, db, cfg)
	sched := scheduler.New(inc, bf, cfg)

	if err := sched.Start(); err != nil {
		log.Fatalf("scheduler start: %v", err)
	}

	log.Printf("Scheduler started. Incremental: %s  Backfill: %s", cfg.Cron.Schedule, cfg.Cron.BackfillSchedule)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down...")
	sched.Stop()
}

func runBackfill(
	cfg *config.Config,
	kiteProvider *kiteprovider.KiteProvider,
	r2 *r2client.Client,
	db *sqlite.DB,
	args []string,
) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	assetType := fs.String("type", "", "equity|futures|options")
	interval := fs.String("interval", "day", "day|15min")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *assetType == "" {
		log.Fatal("--type is required: equity, futures, or options")
	}

	var startDateStr string
	switch *assetType {
	case "futures":
		startDateStr = cfg.Extraction.Futures.BackfillFrom[*interval]
	case "options":
		startDateStr = cfg.Extraction.Options.BackfillFrom[*interval]
	default:
		startDateStr = cfg.Extraction.Equity.BackfillFrom[*interval]
	}

	startDate, err := time.Parse("2006-01-02", startDateStr)
	if err != nil {
		log.Fatalf("invalid backfill_from date %q: %v", startDateStr, err)
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	bf := extractor.NewBackfiller(kiteProvider, r2, db, cfg)
	bcfg := extractor.BackfillConfig{
		Type:      *assetType,
		Interval:  *interval,
		StartDate: startDate,
		EndDate:   lastTradingDay(today),
	}

	log.Printf("Starting backfill: type=%s interval=%s from=%s", *assetType, *interval, startDate.Format("2006-01-02"))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := bf.Run(ctx, bcfg); err != nil {
		log.Fatalf("backfill failed: %v", err)
	}

	log.Println("Backfill complete.")
}

func runTokenRefresh() {
	apiKey := mustEnv("KITE_API_KEY")
	apiSecret := mustEnv("KITE_API_SECRET")
	userID := mustEnv("KITE_USER_ID")
	password := mustEnv("KITE_PASSWORD")
	totpSecret := mustEnv("KITE_TOTP_SECRET")

	fmt.Println("Starting automated Kite login...")

	accessToken, err := kiteprovider.AutoLogin(apiKey, apiSecret, userID, password, totpSecret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\nAccess token obtained successfully.")
	fmt.Printf("\nSet it in your deployment (e.g. as KITE_ACCESS_TOKEN secret), or locally:\n")
	fmt.Printf("  export KITE_ACCESS_TOKEN=%s\n", accessToken)
}

func buildAutoRefreshConfig() *extractor.AutoRefreshConfig {
	userID := os.Getenv("KITE_USER_ID")
	password := os.Getenv("KITE_PASSWORD")
	totpSecret := os.Getenv("KITE_TOTP_SECRET")

	if userID == "" || password == "" || totpSecret == "" {
		return nil
	}

	return &extractor.AutoRefreshConfig{
		APIKey:     mustEnv("KITE_API_KEY"),
		APISecret:  mustEnv("KITE_API_SECRET"),
		UserID:     userID,
		Password:   password,
		TOTPSecret: totpSecret,
	}
}

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

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
