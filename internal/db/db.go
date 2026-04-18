package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

// migrationsFS embeds all SQL migration files at compile time, keeping the
// binary self-contained. Migration files follow the naming convention:
// {version}_{title}.up.sql and {version}_{title}.down.sql.
//
// To add a new migration, create the next numbered pair of SQL files in this
// directory and they will be applied automatically on the next server startup.
//
// Down migrations are supported for development use (roll back with the
// golang-migrate CLI). In production, prefer restoring from a backup over
// running down migrations.
//
// Development reset: rm data/appx.db && ./appx
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open connects to the SQLite database in dataDir, enables WAL mode and foreign
// keys, and runs any pending migrations. It returns the database connection to
// be shared across the application.
func Open(dataDir string) (*sql.DB, error) {
	dbPath := filepath.Join(dataDir, "appx.db")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("set pragma: %w", err)
		}
	}

	// Restrict database file permissions to owner-only. SQLite creates the file
	// on the first query (above), so Chmod must run after the PRAGMAs execute.
	// This prevents other users on the host from reading session tokens or secrets.
	if err := os.Chmod(dbPath, 0600); err != nil {
		db.Close()
		return nil, fmt.Errorf("chmod db: %w", err)
	}

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// runMigrations applies all pending migrations using golang-migrate. Migration
// state is tracked in the schema_migrations table. Runs are idempotent —
// already-applied migrations are skipped.
func runMigrations(db *sql.DB) error {
	sourceDriver, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	dbDriver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return fmt.Errorf("create migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", dbDriver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}
