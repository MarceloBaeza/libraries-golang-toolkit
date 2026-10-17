// Package migrations applies pending golang-migrate "up" migrations to
// Postgres, typically at service startup.
//
// What happens when a migration fails is an explicit choice (OnFailure):
//
//   - FailureStop (the default): nothing is rolled back. The error is
//     returned and golang-migrate leaves the database marked dirty at the
//     failed version, for a controlled manual repair (`migrate force`).
//   - FailureRollbackToPrevious: the database is returned to the version it
//     had before this run (the last version that worked with the previous
//     release), reverting only the migrations applied by this run.
//   - FailureRollbackToZero: every migration is reverted down to version 0.
//     This DROPS ALL DATA managed by the migrations; use it only on
//     disposable databases.
//
// In every case the run still returns an error, so the caller can abort
// startup. A database that is already dirty is never touched: Up returns
// ErrDirty whatever the policy. A run stopped by cancelling its context is
// never rolled back either.
//
// The rollback policies assume each up file is atomic. golang-migrate's
// postgres driver sends a file as one multi-statement query, which
// PostgreSQL runs in a single implicit transaction, so a failed file leaves
// no partial changes. That no longer holds for files with explicit
// BEGIN/COMMIT or statements that cannot run inside a transaction (e.g.
// CREATE INDEX CONCURRENTLY): keep FailureStop for those.
package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
)

// FailurePolicy decides what Up does after a migration fails.
type FailurePolicy int

const (
	// FailureStop rolls nothing back; the database stays dirty at the
	// failed version for manual repair. It is the zero value.
	FailureStop FailurePolicy = iota
	// FailureRollbackToPrevious returns the database to the version it
	// had when Up started.
	FailureRollbackToPrevious
	// FailureRollbackToZero reverts every migration (drops all data).
	FailureRollbackToZero
)

func (p FailurePolicy) String() string {
	switch p {
	case FailureStop:
		return "stop"
	case FailureRollbackToPrevious:
		return "rollback_to_previous"
	case FailureRollbackToZero:
		return "rollback_to_zero"
	default:
		return fmt.Sprintf("FailurePolicy(%d)", int(p))
	}
}

// ErrDirty means a previous migration failed midway and was not rolled
// back; a human must inspect the schema and run `migrate force` first.
var ErrDirty = errors.New("migrations: database is in a dirty migration state; manual repair required")

// Options configure Up. Exactly one of SourceURL or FS must be set.
type Options struct {
	// SourceURL is a golang-migrate source URL, e.g. "file://migrations".
	SourceURL string
	// FS holds NNNNNN_name.up.sql/.down.sql files at its root, e.g. an
	// embed.FS (use fs.Sub for a subdirectory).
	FS fs.FS
	// OnFailure defaults to FailureStop.
	OnFailure FailurePolicy
	// Logger receives progress lines; nil discards them.
	Logger *slog.Logger
}

// Result describes what Up did. Versions are 0 when the database had no
// migration applied.
type Result struct {
	FromVersion uint // version when Up started
	ToVersion   uint // version when Up returned
	// FailedVersion is the migration that failed (0 when none did, or
	// when the failure happened outside a migration file).
	FailedVersion uint
	// RolledBack reports that the failure policy reverted migrations
	// successfully; ToVersion is then the version it reverted to.
	RolledBack bool
}

// Applied reports whether the database ended at a different version.
func (r Result) Applied() bool { return r.ToVersion != r.FromVersion }

// Run applies all pending migrations from migrationsDir (a golang-migrate
// source URL such as "file://migrations") with the default FailureStop
// policy: on failure it returns the error WITHOUT rolling anything back.
//
// Earlier versions called m.Down() on failure, which reverted every
// migration and dropped production data. Callers that really want a
// rollback must now ask for it through Up and Options.OnFailure.
func Run(dsn, migrationsDir string) error {
	_, err := Up(context.Background(), dsn, Options{SourceURL: migrationsDir})
	return err
}

// Up applies all pending migrations and, on failure, applies
// opts.OnFailure. It never calls log.Fatalf/os.Exit: fail-fast is left to
// the caller. Cancelling ctx stops between migration files. Concurrent
// callers serialize on the driver's advisory lock.
func Up(ctx context.Context, dsn string, opts Options) (Result, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.OnFailure < FailureStop || opts.OnFailure > FailureRollbackToZero {
		return Result{}, fmt.Errorf("migrations: unknown failure policy %d", int(opts.OnFailure))
	}

	src, err := openSource(opts)
	if err != nil {
		return Result{}, err
	}
	m, sqlDB, err := newMigrate(ctx, dsn, src)
	if err != nil {
		_ = src.Close()
		return Result{}, err
	}
	defer sqlDB.Close()
	defer m.Close()

	from, dirty, err := version(m)
	if err != nil {
		return Result{}, err
	}
	res := Result{FromVersion: from, ToVersion: from}
	if dirty {
		return res, fmt.Errorf("%w (version %d)", ErrDirty, from)
	}

	stop := context.AfterFunc(ctx, func() { m.GracefulStop <- true })
	defer stop()

	log.Info("migrations: applying pending", slog.Uint64("from_version", uint64(from)))
	upErr := m.Up()
	stop() // a rollback must not be cut short by the same cancellation
	if upErr == nil || errors.Is(upErr, migrate.ErrNoChange) {
		to, _, err := version(m)
		if err != nil {
			return res, err
		}
		res.ToVersion = to
		log.Info("migrations: up to date", slog.Uint64("version", uint64(to)), slog.Bool("applied", res.Applied()))
		return res, nil
	}

	at, dirtyAfter, err := version(m)
	if err != nil {
		return res, errors.Join(fmt.Errorf("migrations: apply failed: %w", upErr), err)
	}
	res.ToVersion = at
	if dirtyAfter {
		res.FailedVersion = at
	}
	failErr := fmt.Errorf("migrations: apply failed at version %d (dirty=%t): %w", at, dirtyAfter, upErr)

	if opts.OnFailure == FailureStop || ctx.Err() != nil {
		log.Error("migrations: failed; no rollback",
			slog.String("policy", opts.OnFailure.String()), slog.Bool("cancelled", ctx.Err() != nil),
			slog.Uint64("version", uint64(at)), slog.Bool("dirty", dirtyAfter), slog.Any("error", upErr))
		return res, failErr
	}

	target := uint(0)
	if opts.OnFailure == FailureRollbackToPrevious {
		target = from
	}
	log.Warn("migrations: failed; rolling back",
		slog.String("policy", opts.OnFailure.String()), slog.Uint64("failed_version", uint64(res.FailedVersion)),
		slog.Uint64("target_version", uint64(target)), slog.Any("error", upErr))

	to, rbErr := rollback(m, src, at, dirtyAfter, target)
	res.ToVersion = to
	if rbErr != nil {
		log.Error("migrations: rollback failed; manual repair required",
			slog.Uint64("version", uint64(to)), slog.Any("error", rbErr))
		return res, errors.Join(failErr, fmt.Errorf("migrations: rollback to version %d failed: %w", target, rbErr))
	}
	res.RolledBack = true
	log.Warn("migrations: rolled back", slog.Uint64("version", uint64(to)))
	return res, fmt.Errorf("%w; rolled back to version %d", failErr, to)
}

// rollback clears the dirty flag of the failed (atomic, hence unapplied)
// migration and then runs the down migrations until target. It returns the
// version the database ended at.
func rollback(m *migrate.Migrate, src source.Driver, at uint, dirty bool, target uint) (uint, error) {
	if dirty {
		prev, err := src.Prev(at)
		switch {
		case errors.Is(err, os.ErrNotExist):
			err = m.Force(-1) // the failed migration was the first one
		case err != nil:
			return at, fmt.Errorf("find version before %d: %w", at, err)
		default:
			err = m.Force(int(prev))
		}
		if err != nil {
			return at, fmt.Errorf("clear dirty version %d: %w", at, err)
		}
	}

	cur, _, err := version(m)
	if err != nil || cur == target {
		return cur, err
	}
	if target == 0 {
		err = m.Down()
	} else {
		err = m.Migrate(target)
	}
	v, _, verr := version(m)
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return v, err
	}
	return v, verr
}

func newMigrate(ctx context.Context, dsn string, src source.Driver) (*migrate.Migrate, *sql.DB, error) {
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("migrations: open db: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("migrations: ping: %w", err)
	}
	driver, err := postgres.WithInstance(sqlDB, &postgres.Config{})
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("migrations: driver: %w", err)
	}
	m, err := migrate.NewWithInstance("source", src, "postgres", driver)
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("migrations: init: %w", err)
	}
	return m, sqlDB, nil
}

func openSource(opts Options) (source.Driver, error) {
	var (
		src source.Driver
		err error
	)
	switch {
	case opts.FS != nil && opts.SourceURL != "":
		return nil, errors.New("migrations: set either SourceURL or FS, not both")
	case opts.FS != nil:
		src, err = iofs.New(opts.FS, ".")
	case opts.SourceURL != "":
		src, err = source.Open(opts.SourceURL)
	default:
		return nil, errors.New("migrations: no source: set SourceURL or FS")
	}
	if err != nil {
		return nil, fmt.Errorf("migrations: source: %w", err)
	}
	return src, nil
}

// version reads the current version; a database with no migration
// applied reports 0.
func version(m *migrate.Migrate) (uint, bool, error) {
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("migrations: read version: %w", err)
	}
	return v, dirty, nil
}
