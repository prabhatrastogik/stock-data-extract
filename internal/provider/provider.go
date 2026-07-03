package provider

import (
	"context"
	"time"
)

// Candle is the canonical OHLC type used throughout the system.
type Candle struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume int64
	OI     int64
}

// Instrument describes a tradable instrument from the provider's master list.
type Instrument struct {
	Token          string
	ExchangeToken  int
	TradingSymbol  string
	Name           string
	Exchange       string
	InstrumentType string
	Segment        string
	Expiry         time.Time
	Strike         float64
	LotSize        int
	TickSize       float64
}

// Provider is the single interface all data sources must implement.
type Provider interface {
	Instruments(ctx context.Context, exchange string) ([]Instrument, error)
	Historical(
		ctx context.Context,
		token string,
		interval string,
		from, to time.Time,
		continuous bool,
		oi bool,
	) ([]Candle, error)
}

// RefreshableProvider extends Provider with token lifecycle methods required
// for automated session refresh without restarting the process.
type RefreshableProvider interface {
	Provider
	ValidateToken(ctx context.Context) error
	SetAccessToken(token string)
}
