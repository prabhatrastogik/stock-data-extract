package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const dateFmt = "2006-01-02"

func (d *DB) GetLastDate(exchange, symbol, interval string) (time.Time, bool, error) {
	var s string
	err := d.db.QueryRow(
		`SELECT last_date FROM extraction_state WHERE exchange=? AND symbol=? AND interval=?`,
		exchange, symbol, interval,
	).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("get last date: %w", err)
	}
	t, err := time.Parse(dateFmt, s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse last date %q: %w", s, err)
	}
	return t, true, nil
}

func (d *DB) SetLastDate(exchange, symbol, interval string, date time.Time) error {
	_, err := d.db.Exec(
		`INSERT INTO extraction_state(exchange, symbol, interval, last_date, updated_at)
		 VALUES(?,?,?,?,datetime('now'))
		 ON CONFLICT(exchange,symbol,interval) DO UPDATE SET
		   last_date=excluded.last_date,
		   updated_at=excluded.updated_at`,
		exchange, symbol, interval, date.Format(dateFmt),
	)
	if err != nil {
		return fmt.Errorf("set last date: %w", err)
	}
	return nil
}

func (d *DB) GetOptionsLastDate(underlying, interval string) (time.Time, bool, error) {
	var s string
	err := d.db.QueryRow(
		`SELECT last_date FROM options_extraction_state WHERE underlying=? AND interval=?`,
		underlying, interval,
	).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("get options last date: %w", err)
	}
	t, err := time.Parse(dateFmt, s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse options last date %q: %w", s, err)
	}
	return t, true, nil
}

func (d *DB) SetOptionsLastDate(underlying, interval string, date time.Time) error {
	_, err := d.db.Exec(
		`INSERT INTO options_extraction_state(underlying, interval, last_date, updated_at)
		 VALUES(?,?,?,datetime('now'))
		 ON CONFLICT(underlying,interval) DO UPDATE SET
		   last_date=excluded.last_date,
		   updated_at=excluded.updated_at`,
		underlying, interval, date.Format(dateFmt),
	)
	if err != nil {
		return fmt.Errorf("set options last date: %w", err)
	}
	return nil
}
