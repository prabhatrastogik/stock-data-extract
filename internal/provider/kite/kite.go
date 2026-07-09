package kite

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	kiteconnect "github.com/zerodha/gokiteconnect/v4"

	"github.com/prabhatrastogik/stock-data-extract/internal/provider"
)

type KiteProvider struct {
	kc             *kiteconnect.Client
	limiter        *Limiter
	initialBackoff time.Duration // 0 → defaults to time.Second; override in tests
}

func New(apiKey, accessToken string, rps int) *KiteProvider {
	kc := kiteconnect.New(apiKey)
	kc.SetAccessToken(accessToken)
	return &KiteProvider{kc: kc, limiter: NewLimiter(rps)}
}

func (p *KiteProvider) SetAccessToken(token string) {
	p.kc.SetAccessToken(token)
}

func (p *KiteProvider) ValidateToken(ctx context.Context) error {
	if err := p.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := p.kc.GetUserProfile()
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	return nil
}

// Historical fetches OHLC candles with automatic retry on rate-limit errors.
func (p *KiteProvider) Historical(
	ctx context.Context,
	token string,
	interval string,
	from, to time.Time,
	continuous bool,
	oi bool,
) ([]provider.Candle, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	tokenInt, err := strconv.Atoi(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token %q: %w", token, err)
	}

	kiteInterval := mapInterval(interval)

	var raw []kiteconnect.HistoricalData
	backoff := p.initialBackoff
	if backoff == 0 {
		backoff = time.Second
	}
	for attempt := 0; attempt < 3; attempt++ {
		raw, err = p.kc.GetHistoricalData(tokenInt, kiteInterval, from, to, continuous, oi)
		if err == nil {
			break
		}
		if !isRateLimitError(err) {
			return nil, fmt.Errorf("historical data token %s: %w", token, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
	if err != nil {
		return nil, fmt.Errorf("historical data token %s after retries: %w", token, err)
	}

	candles := make([]provider.Candle, 0, len(raw))
	for _, r := range raw {
		candles = append(candles, provider.Candle{
			Time:   r.Date.Time,
			Open:   r.Open,
			High:   r.High,
			Low:    r.Low,
			Close:  r.Close,
			Volume: int64(r.Volume),
			OI:     int64(r.OI),
		})
	}
	return candles, nil
}

func mapInterval(interval string) string {
	if interval == "15min" {
		return "15minute"
	}
	return interval
}

func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "429") || strings.Contains(s, "rate limit") || strings.Contains(s, "Too Many")
}
