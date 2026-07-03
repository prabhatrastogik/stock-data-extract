package parquet

// CandleRecord is used for all equity and futures data.
type CandleRecord struct {
	Timestamp int64   `parquet:"timestamp"`
	Open      float64 `parquet:"open"`
	High      float64 `parquet:"high"`
	Low       float64 `parquet:"low"`
	Close     float64 `parquet:"close"`
	Volume    int64   `parquet:"volume"`
	OI        int64   `parquet:"oi"`
}

// OptionCandleRecord is used for the by-underlying options model.
type OptionCandleRecord struct {
	Timestamp  int64   `parquet:"timestamp"`
	Expiry     string  `parquet:"expiry"`
	Strike     float64 `parquet:"strike"`
	OptionType string  `parquet:"option_type"`
	Open       float64 `parquet:"open"`
	High       float64 `parquet:"high"`
	Low        float64 `parquet:"low"`
	Close      float64 `parquet:"close"`
	Volume     int64   `parquet:"volume"`
	OI         int64   `parquet:"oi"`
}

// InstrumentRecord is stored in the daily instruments snapshot Parquet.
type InstrumentRecord struct {
	Token          int64   `parquet:"instrument_token"`
	ExchangeToken  int64   `parquet:"exchange_token"`
	TradingSymbol  string  `parquet:"tradingsymbol"`
	Name           string  `parquet:"name"`
	Exchange       string  `parquet:"exchange"`
	InstrumentType string  `parquet:"instrument_type"`
	Segment        string  `parquet:"segment"`
	Expiry         string  `parquet:"expiry"`
	Strike         float64 `parquet:"strike"`
	LotSize        int64   `parquet:"lot_size"`
	TickSize       float64 `parquet:"tick_size"`
}
