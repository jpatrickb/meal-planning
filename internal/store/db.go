// Package store owns the SQLite connection and schema migrations. Mutating
// domain logic elsewhere takes a *sql.Tx (never a bare *sql.DB) so that every
// write is structurally forced through an explicit transaction — this is the
// direct fix for the old markdown system's silent-no-op persistence bug.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const dbFileName = "mealplanning.db"

// Open ensures dataRoot exists, opens (creating if needed) the SQLite database
// inside it, applies any pending migrations, and returns a ready-to-use handle.
func Open(dataRoot string) (*sql.DB, error) {
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("creating data root %s: %w", dataRoot, err)
	}

	dbPath := filepath.Join(dataRoot, dbFileName)
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		dbPath,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database at %s: %w", dbPath, err)
	}
	// WAL mode allows concurrent readers, so a small pool is fine and avoids
	// deadlocking any code path that runs a nested query while an outer
	// *sql.Rows cursor is still open (writes still serialize via SQLite's own
	// locking plus busy_timeout above).
	db.SetMaxOpenConns(4)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating database: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("reading schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("starting transaction for %s: %w", name, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s: %w", name, err)
		}
	}
	return nil
}
