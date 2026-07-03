package kite

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/prabhatrastogik/stock-data-extract/internal/provider"
)

func (p *KiteProvider) Instruments(ctx context.Context, exchange string) ([]provider.Instrument, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	raw, err := p.kc.GetInstrumentsByExchange(exchange)
	backoff := p.initialBackoff
	if backoff == 0 {
		backoff = time.Second
	}
	for attempt := 1; attempt < 3 && err != nil; attempt++ {
		if !isRateLimitError(err) {
			return nil, fmt.Errorf("kite instruments %s: %w", exchange, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
		raw, err = p.kc.GetInstrumentsByExchange(exchange)
	}
	if err != nil {
		return nil, fmt.Errorf("kite instruments %s after retries: %w", exchange, err)
	}

	out := make([]provider.Instrument, 0, len(raw))
	for _, r := range raw {
		inst := provider.Instrument{
			Token:          strconv.FormatUint(uint64(r.InstrumentToken), 10),
			ExchangeToken:  r.ExchangeToken,
			TradingSymbol:  r.Tradingsymbol,
			Name:           r.Name,
			Exchange:       r.Exchange,
			InstrumentType: r.InstrumentType,
			Segment:        r.Segment,
			Strike:         r.StrikePrice,
			LotSize:        int(r.LotSize),
			TickSize:       r.TickSize,
		}
		// Expiry is zero for equity; only set for derivatives
		if r.Expiry.Time.Year() > 1 {
			inst.Expiry = r.Expiry.Time
		}
		out = append(out, inst)
	}
	return out, nil
}
