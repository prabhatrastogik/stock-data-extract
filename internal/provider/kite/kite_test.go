package kite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestProvider creates a KiteProvider wired to handler via an httptest.Server.
// The server is closed automatically when t ends.
func newTestProvider(t *testing.T, handler http.Handler) *KiteProvider {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	p := New("api-key", "access-token", 100) // high RPS so limiter never blocks
	p.kc.SetBaseURI(ts.URL)
	p.initialBackoff = time.Millisecond // fast retries in tests
	return p
}

func successEnvelope(jsonData string) string {
	return fmt.Sprintf(`{"status":"success","data":%s}`, jsonData)
}

func errorEnvelope(errType, msg string) string {
	return fmt.Sprintf(`{"status":"error","error_type":%q,"message":%q}`, errType, msg)
}

// ---------------------------------------------------------------------------
// ValidateToken
// ---------------------------------------------------------------------------

func TestValidateToken_Success(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/profile" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, successEnvelope(`{"user_id":"U1","user_name":"Test","email":"t@t.com","broker":"ZERODHA","products":[],"order_types":[],"exchanges":[]}`))
	}))

	if err := p.ValidateToken(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateToken_TokenError(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, errorEnvelope("TokenException", "Invalid token"))
	}))

	err := p.ValidateToken(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "token validation failed") {
		t.Errorf("want 'token validation failed' in error, got: %v", err)
	}
}

func TestValidateToken_ContextCancelled(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("HTTP call should not be made after context cancel")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.ValidateToken(ctx); err == nil {
		t.Fatal("expected context error")
	}
}

// ---------------------------------------------------------------------------
// Instruments
// ---------------------------------------------------------------------------

const instrumentsCSV = `instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange
128083204,500325,RELIANCE,RELIANCE INDUSTRIES,2500.00,,0,0.05,1,EQ,NSE-EQ,NSE
408065,1594,INFY,INFOSYS,1600.00,,0,0.05,1,EQ,NSE-EQ,NSE`

func TestInstruments_Success(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/instruments/NSE" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, instrumentsCSV)
	}))

	insts, err := p.Instruments(context.Background(), "NSE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 instruments, got %d", len(insts))
	}

	rel := insts[0]
	if rel.TradingSymbol != "RELIANCE" {
		t.Errorf("TradingSymbol: want RELIANCE, got %s", rel.TradingSymbol)
	}
	if rel.Token != "128083204" {
		t.Errorf("Token: want 128083204, got %s", rel.Token)
	}
	if rel.Exchange != "NSE" {
		t.Errorf("Exchange: want NSE, got %s", rel.Exchange)
	}
	if rel.InstrumentType != "EQ" {
		t.Errorf("InstrumentType: want EQ, got %s", rel.InstrumentType)
	}
	if !rel.Expiry.IsZero() {
		t.Errorf("Expiry: want zero for equity, got %v", rel.Expiry)
	}
}

func TestInstruments_DerivativeExpiry(t *testing.T) {
	csv := `instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange
2939649,11483,NIFTY24JAN18000CE,NIFTY,0,2024-01-25,18000,0.05,50,CE,NFO-OPT,NFO`

	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, csv)
	}))

	insts, err := p.Instruments(context.Background(), "NFO")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("expected 1 instrument, got %d", len(insts))
	}
	inst := insts[0]
	if inst.Expiry.IsZero() {
		t.Error("Expiry: want non-zero for derivative, got zero")
	}
	if inst.Strike != 18000 {
		t.Errorf("Strike: want 18000, got %f", inst.Strike)
	}
	if inst.InstrumentType != "CE" {
		t.Errorf("InstrumentType: want CE, got %s", inst.InstrumentType)
	}
}

func TestInstruments_EmptyExchange(t *testing.T) {
	csv := `instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange`

	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, csv)
	}))

	insts, err := p.Instruments(context.Background(), "NSE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(insts) != 0 {
		t.Errorf("expected 0 instruments for empty CSV, got %d", len(insts))
	}
}

func TestInstruments_RequestURL(t *testing.T) {
	var gotPath string
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, `instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange`)
	}))

	_, _ = p.Instruments(context.Background(), "NFO")

	if want := "/instruments/NFO"; gotPath != want {
		t.Errorf("path: want %s, got %s", want, gotPath)
	}
}

// ---------------------------------------------------------------------------
// Historical
// ---------------------------------------------------------------------------

const singleCandleBody = `{"status":"success","data":{"candles":[["2024-01-15T09:15:00+0530",100.0,110.0,95.0,105.0,1000,500]]}}`

func TestHistorical_Success(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/instruments/historical/12345/day" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, singleCandleBody)
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	candles, err := p.Historical(context.Background(), "12345", "day", from, from, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(candles))
	}
	c := candles[0]
	if c.Open != 100.0 {
		t.Errorf("Open: want 100, got %f", c.Open)
	}
	if c.High != 110.0 {
		t.Errorf("High: want 110, got %f", c.High)
	}
	if c.Low != 95.0 {
		t.Errorf("Low: want 95, got %f", c.Low)
	}
	if c.Close != 105.0 {
		t.Errorf("Close: want 105, got %f", c.Close)
	}
	if c.Volume != 1000 {
		t.Errorf("Volume: want 1000, got %d", c.Volume)
	}
	if c.OI != 500 {
		t.Errorf("OI: want 500, got %d", c.OI)
	}
}

func TestHistorical_IntervalMappedTo15Minute(t *testing.T) {
	var gotPath string
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"candles":[]}}`)
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	if _, err := p.Historical(context.Background(), "12345", "15min", from, from, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := "/instruments/historical/12345/15minute"; gotPath != want {
		t.Errorf("path: want %s, got %s", want, gotPath)
	}
}

func TestHistorical_OIAndContinuousParams(t *testing.T) {
	var gotQuery string
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"candles":[]}}`)
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	if _, err := p.Historical(context.Background(), "99999", "day", from, from, true, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotQuery, "continuous=1") {
		t.Errorf("expected continuous=1 in query, got: %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "oi=1") {
		t.Errorf("expected oi=1 in query, got: %s", gotQuery)
	}
}

func TestHistorical_InvalidToken(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("HTTP call should not be made for invalid token")
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	_, err := p.Historical(context.Background(), "not-a-number", "day", from, from, false, false)
	if err == nil {
		t.Fatal("expected error for non-numeric token")
	}
	if !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHistorical_RateLimitRetries(t *testing.T) {
	var calls int32
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			// First two attempts hit rate limit.
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, errorEnvelope("GeneralException", "Too Many Requests"))
			return
		}
		fmt.Fprint(w, singleCandleBody)
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	candles, err := p.Historical(context.Background(), "12345", "day", from, from, false, false)
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if len(candles) != 1 {
		t.Errorf("expected 1 candle after retry, got %d", len(candles))
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 HTTP calls (2 rate-limited + 1 success), got %d", got)
	}
}

func TestHistorical_RateLimitExhausted(t *testing.T) {
	var calls int32
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, errorEnvelope("GeneralException", "Too Many Requests"))
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	_, err := p.Historical(context.Background(), "12345", "day", from, from, false, false)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "after retries") {
		t.Errorf("unexpected error message: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 HTTP calls, got %d", got)
	}
}

func TestHistorical_NonRateLimitErrorNoRetry(t *testing.T) {
	var calls int32
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, errorEnvelope("TokenException", "Invalid token"))
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	if _, err := p.Historical(context.Background(), "12345", "day", from, from, false, false); err == nil {
		t.Fatal("expected error")
	}
	// Must not retry for non-rate-limit errors.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 HTTP call (no retry), got %d", got)
	}
}

func TestHistorical_EmptyCandles(t *testing.T) {
	p := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"candles":[]}}`)
	}))

	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	candles, err := p.Historical(context.Background(), "12345", "day", from, from, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candles) != 0 {
		t.Errorf("expected 0 candles, got %d", len(candles))
	}
}

// ---------------------------------------------------------------------------
// Unit tests for internal helpers
// ---------------------------------------------------------------------------

func TestMapInterval(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"15min", "15minute"},
		{"day", "day"},
		{"minute", "minute"},
		{"60minute", "60minute"},
	}
	for _, tt := range tests {
		if got := mapInterval(tt.input); got != tt.want {
			t.Errorf("mapInterval(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"contains 429", errors.New("HTTP 429 error"), true},
		{"contains rate limit", errors.New("rate limit exceeded"), true},
		{"contains Too Many", errors.New("Too Many Requests"), true},
		{"unrelated error", errors.New("invalid token"), false},
		{"empty message", errors.New(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimitError(tt.err); got != tt.want {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
