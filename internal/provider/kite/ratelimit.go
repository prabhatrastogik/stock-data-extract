package kite

import (
	"context"

	"golang.org/x/time/rate"
)

type Limiter struct {
	inner *rate.Limiter
}

func NewLimiter(rps int) *Limiter {
	// Burst of 1 prevents thundering-herd on startup; this is a batch pipeline,
	// not a latency-sensitive service, so steady-state throughput is what matters.
	return &Limiter{inner: rate.NewLimiter(rate.Limit(rps), 1)}
}

func (l *Limiter) Wait(ctx context.Context) error {
	return l.inner.Wait(ctx)
}
