package parquet

import (
	"bytes"
	"context"
	"fmt"
	"time"

	goparquet "github.com/parquet-go/parquet-go"

	r2client "github.com/prabhatrastogik/stock-data-extract/internal/storage/r2"
)

// ReadCandles downloads the Parquet file at key and returns rows filtered to [from, to] inclusive.
// Returns empty slice if key does not exist.
func ReadCandles(ctx context.Context, r2 r2client.BlobStore, key string, from, to time.Time) ([]CandleRecord, error) {
	data, err := r2.Download(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	if data == nil {
		return nil, nil
	}

	all, err := goparquet.Read[CandleRecord](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("parse parquet %s: %w", key, err)
	}

	fromUs := from.UnixMicro()
	toUs := to.UnixMicro()

	out := make([]CandleRecord, 0, len(all))
	for _, r := range all {
		if r.Timestamp >= fromUs && r.Timestamp <= toUs {
			out = append(out, r)
		}
	}
	return out, nil
}

// ReadOptionCandles reads options data filtered by time range and optional expiry.
// Pass zero time.Time for expiry to return all expiries.
func ReadOptionCandles(
	ctx context.Context,
	r2 r2client.BlobStore,
	key string,
	from, to time.Time,
	expiry time.Time,
) ([]OptionCandleRecord, error) {
	data, err := r2.Download(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	if data == nil {
		return nil, nil
	}

	all, err := goparquet.Read[OptionCandleRecord](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("parse parquet %s: %w", key, err)
	}

	fromUs := from.UnixMicro()
	toUs := to.UnixMicro()
	expiryStr := ""
	if !expiry.IsZero() {
		expiryStr = expiry.Format("2006-01-02")
	}

	out := make([]OptionCandleRecord, 0, len(all))
	for _, r := range all {
		if r.Timestamp < fromUs || r.Timestamp > toUs {
			continue
		}
		if expiryStr != "" && r.Expiry != expiryStr {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
