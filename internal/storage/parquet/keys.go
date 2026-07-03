package parquet

import (
	"fmt"
	"time"
)

func EquityDayKey(symbol, year string) string {
	return fmt.Sprintf("NSE/EQ/%s/day/%s.parquet", symbol, year)
}

func Equity15MinKey(symbol, yearMonth string) string {
	return fmt.Sprintf("NSE/EQ/%s/15min/%s.parquet", symbol, yearMonth)
}

func FuturesDayKey(symbol, year string) string {
	return fmt.Sprintf("NFO/FUT/%s/day/%s.parquet", symbol, year)
}

func Futures15MinKey(symbol, yearMonth string) string {
	return fmt.Sprintf("NFO/FUT/%s/15min/%s.parquet", symbol, yearMonth)
}

func OptionsDayKey(underlying, year string) string {
	return fmt.Sprintf("NFO/OPT/%s/day/%s.parquet", underlying, year)
}

func Options15MinKey(underlying, yearMonth string) string {
	return fmt.Sprintf("NFO/OPT/%s/15min/%s.parquet", underlying, yearMonth)
}

func InstrumentsSnapshotKey(date time.Time) string {
	return fmt.Sprintf("instruments/snapshots/%s.parquet", date.Format("2006-01-02"))
}
