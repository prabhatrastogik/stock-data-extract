package sqlite

import (
	"fmt"
	"time"

	pq "github.com/prabhatrastogik/stock-data-extract/internal/storage/parquet"
)

func (d *DB) UpsertInstruments(date time.Time, records []pq.InstrumentRecord) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin upsert instruments tx: %w", err)
	}

	dateStr := date.Format("2006-01-02")
	stmt, err := tx.Prepare(`
		INSERT INTO instruments
			(snapshot_date, instrument_token, exchange_token, exchange, tradingsymbol, name,
			 instrument_type, segment, expiry, strike, lot_size, tick_size)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(snapshot_date, instrument_token) DO UPDATE SET
			exchange_token=excluded.exchange_token,
			exchange=excluded.exchange,
			tradingsymbol=excluded.tradingsymbol,
			name=excluded.name,
			instrument_type=excluded.instrument_type,
			segment=excluded.segment,
			expiry=excluded.expiry,
			strike=excluded.strike,
			lot_size=excluded.lot_size,
			tick_size=excluded.tick_size
	`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare upsert instruments: %w", err)
	}
	defer stmt.Close()

	for _, r := range records {
		if _, err := stmt.Exec(
			dateStr, r.Token, r.ExchangeToken, r.Exchange, r.TradingSymbol, r.Name,
			r.InstrumentType, r.Segment, r.Expiry, r.Strike, r.LotSize, r.TickSize,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("upsert instrument %s: %w", r.TradingSymbol, err)
		}
	}

	return tx.Commit()
}

func (d *DB) LatestInstruments(exchange, instrumentType string) ([]pq.InstrumentRecord, error) {
	rows, err := d.db.Query(`
		SELECT instrument_token, exchange_token, tradingsymbol, name, exchange,
		       instrument_type, segment, expiry, strike, lot_size, tick_size
		FROM instruments
		WHERE snapshot_date = (SELECT MAX(snapshot_date) FROM instruments)
		  AND exchange = ?
		  AND instrument_type = ?
	`, exchange, instrumentType)
	if err != nil {
		return nil, fmt.Errorf("query latest instruments: %w", err)
	}
	defer rows.Close()

	return scanInstruments(rows)
}

func (d *DB) SearchInstruments(query string, limit int) ([]pq.InstrumentRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	pattern := "%" + query + "%"
	rows, err := d.db.Query(`
		SELECT instrument_token, exchange_token, tradingsymbol, name, exchange,
		       instrument_type, segment, expiry, strike, lot_size, tick_size
		FROM instruments
		WHERE snapshot_date = (SELECT MAX(snapshot_date) FROM instruments)
		  AND (tradingsymbol LIKE ? OR name LIKE ?)
		LIMIT ?
	`, pattern, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search instruments: %w", err)
	}
	defer rows.Close()

	return scanInstruments(rows)
}

func (d *DB) OptionExpiries(underlying string) ([]time.Time, error) {
	rows, err := d.db.Query(`
		SELECT DISTINCT expiry
		FROM instruments
		WHERE snapshot_date = (SELECT MAX(snapshot_date) FROM instruments)
		  AND tradingsymbol LIKE (? || '%')
		  AND instrument_type IN ('CE','PE')
		  AND expiry != ''
		ORDER BY expiry
	`, underlying)
	if err != nil {
		return nil, fmt.Errorf("query option expiries: %w", err)
	}
	defer rows.Close()

	var expiries []time.Time
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			continue
		}
		expiries = append(expiries, t)
	}
	return expiries, rows.Err()
}

func scanInstruments(rows interface {
	Next() bool
	Scan(...interface{}) error
	Err() error
}) ([]pq.InstrumentRecord, error) {
	var out []pq.InstrumentRecord
	for rows.Next() {
		var r pq.InstrumentRecord
		if err := rows.Scan(
			&r.Token, &r.ExchangeToken, &r.TradingSymbol, &r.Name, &r.Exchange,
			&r.InstrumentType, &r.Segment, &r.Expiry, &r.Strike, &r.LotSize, &r.TickSize,
		); err != nil {
			return nil, fmt.Errorf("scan instrument: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
