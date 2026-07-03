package parquet

import (
	"testing"
	"time"
)

func TestKeys(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"EquityDayKey", EquityDayKey("INFY", "2024"), "NSE/EQ/INFY/day/2024.parquet"},
		{"Equity15MinKey", Equity15MinKey("INFY", "2024-01"), "NSE/EQ/INFY/15min/2024-01.parquet"},
		{"FuturesDayKey", FuturesDayKey("NIFTY24JANFUT", "2024"), "NFO/FUT/NIFTY24JANFUT/day/2024.parquet"},
		{"Futures15MinKey", Futures15MinKey("NIFTY24JANFUT", "2024-01"), "NFO/FUT/NIFTY24JANFUT/15min/2024-01.parquet"},
		{"OptionsDayKey", OptionsDayKey("NIFTY", "2024"), "NFO/OPT/NIFTY/day/2024.parquet"},
		{"Options15MinKey", Options15MinKey("NIFTY", "2024-01"), "NFO/OPT/NIFTY/15min/2024-01.parquet"},
		{
			"InstrumentsSnapshotKey",
			InstrumentsSnapshotKey(time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)),
			"instruments/snapshots/2024-01-15.parquet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
