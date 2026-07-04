package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// migrationVersion parses the leading integer from a filename like "003_foo.sql".
func migrationVersion(name string) (int, error) {
	var v int
	if _, err := fmt.Sscanf(name, "%d", &v); err != nil {
		return 0, fmt.Errorf("migration filename %q must start with a number: %w", name, err)
	}
	return v, nil
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	// wal_autocheckpoint=0: disable SQLite's built-in auto-checkpoint so Litestream
	// can manage checkpointing itself. Without this, SQLite can move WAL frames to
	// the main DB mid-snapshot, producing non-sequential page numbers in the R2 backup.
	// busy_timeout: prevents lock errors when Litestream holds a read lock during snapshot.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	store := &DB{db: db}
	if err := store.runMigrations(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}

	return store, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) runMigrations() error {
	// Bootstrap schema_migrations inline so version tracking works before any
	// file-based migration runs. Migration 004 defines the same schema; it is a safe no-op.
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("bootstrap schema_migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}

		var applied int
		row := d.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version)
		if err := row.Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied > 0 {
			continue
		}

		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		tx, err := d.db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx migration %d: %w", version, err)
		}

		for _, stmt := range splitStatements(string(data)) {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("execute migration %d: %w", version, err)
			}
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations(version) VALUES(?)", version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}

	return nil
}

func splitStatements(sql string) []string {
	var out []string
	for _, s := range strings.Split(sql, ";") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
