package parquet

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	goparquet "github.com/parquet-go/parquet-go"

	r2client "github.com/prabhatrastogik/stock-data-extract/internal/storage/r2"
)

// AppendCandles merges newRecords into the existing Parquet file at key.
// Creates the file if it does not exist. Deduplicates on Timestamp.
// Rows are sorted ascending by Timestamp before upload.
func AppendCandles(ctx context.Context, r2 r2client.BlobStore, key string, newRecords []CandleRecord) error {
	existing, err := readCandles(ctx, r2, key)
	if err != nil {
		return err
	}

	merged := mergeCandles(existing, newRecords)

	return writeCandles(ctx, r2, key, merged)
}

// AppendOptionCandles is the equivalent for OptionCandleRecord.
// Deduplicates on (Timestamp, Expiry, Strike, OptionType).
func AppendOptionCandles(ctx context.Context, r2 r2client.BlobStore, key string, newRecords []OptionCandleRecord) error {
	existing, err := readOptionCandles(ctx, r2, key)
	if err != nil {
		return err
	}

	merged := mergeOptionCandles(existing, newRecords)

	return writeOptionCandles(ctx, r2, key, merged)
}

func mergeCandles(existing, incoming []CandleRecord) []CandleRecord {
	seen := make(map[int64]CandleRecord, len(existing)+len(incoming))
	for _, r := range existing {
		seen[r.Timestamp] = r
	}
	for _, r := range incoming {
		seen[r.Timestamp] = r
	}
	out := make([]CandleRecord, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

type optionKey struct {
	Timestamp  int64
	Expiry     string
	Strike     float64
	OptionType string
}

func mergeOptionCandles(existing, incoming []OptionCandleRecord) []OptionCandleRecord {
	seen := make(map[optionKey]OptionCandleRecord, len(existing)+len(incoming))
	for _, r := range existing {
		seen[optionKey{r.Timestamp, r.Expiry, r.Strike, r.OptionType}] = r
	}
	for _, r := range incoming {
		seen[optionKey{r.Timestamp, r.Expiry, r.Strike, r.OptionType}] = r
	}
	out := make([]OptionCandleRecord, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp != out[j].Timestamp {
			return out[i].Timestamp < out[j].Timestamp
		}
		if out[i].Expiry != out[j].Expiry {
			return out[i].Expiry < out[j].Expiry
		}
		if out[i].Strike != out[j].Strike {
			return out[i].Strike < out[j].Strike
		}
		return out[i].OptionType < out[j].OptionType
	})
	return out
}

func readCandles(ctx context.Context, r2 r2client.BlobStore, key string) ([]CandleRecord, error) {
	data, err := r2.Download(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	if data == nil {
		return nil, nil
	}
	records, err := goparquet.Read[CandleRecord](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("parse parquet %s: %w", key, err)
	}
	return records, nil
}

func readOptionCandles(ctx context.Context, r2 r2client.BlobStore, key string) ([]OptionCandleRecord, error) {
	data, err := r2.Download(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	if data == nil {
		return nil, nil
	}
	records, err := goparquet.Read[OptionCandleRecord](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("parse parquet %s: %w", key, err)
	}
	return records, nil
}

func writeCandles(ctx context.Context, r2 r2client.BlobStore, key string, records []CandleRecord) error {
	var buf bytes.Buffer
	if err := goparquet.Write(&buf, records); err != nil {
		return fmt.Errorf("serialize parquet %s: %w", key, err)
	}
	return r2.Upload(ctx, key, buf.Bytes(), "application/octet-stream")
}

func writeOptionCandles(ctx context.Context, r2 r2client.BlobStore, key string, records []OptionCandleRecord) error {
	var buf bytes.Buffer
	if err := goparquet.Write(&buf, records); err != nil {
		return fmt.Errorf("serialize parquet %s: %w", key, err)
	}
	return r2.Upload(ctx, key, buf.Bytes(), "application/octet-stream")
}

// WriteInstruments writes an InstrumentRecord slice to R2 (no merge — full overwrite).
func WriteInstruments(ctx context.Context, r2 r2client.BlobStore, key string, records []InstrumentRecord) error {
	var buf bytes.Buffer
	if err := goparquet.Write(&buf, records); err != nil {
		return fmt.Errorf("serialize instruments parquet: %w", err)
	}
	return r2.Upload(ctx, key, buf.Bytes(), "application/octet-stream")
}
