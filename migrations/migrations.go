package migrations

import (
	"database/sql"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
)

// Run applies all pending "up" migrations found in migrationsDir against the
// Postgres database reachable at dsn. On failure it attempts a rollback before
// returning the error, leaving fail-fast behavior (log.Fatalf, os.Exit) to the caller.
func Run(dsn, migrationsDir string) error {
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("migrations: open db: %w", err)
	}
	defer sqlDB.Close()

	driver, err := postgres.WithInstance(sqlDB, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("migrations: driver: %w", err)
	}
	m, err := migrate.NewWithDatabaseInstance(migrationsDir, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrations: init: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		_ = m.Down()
		return fmt.Errorf("migrations: apply failed, rolled back: %w", err)
	}
	return nil
}
