package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/MarceloBaeza/libraries-golang-toolkit/internal/pgtest"
)

// Migrations 1 and 2 are valid; 3 creates a table and then fails, so it
// also shows that a failed file leaves no partial changes.
var (
	good1 = map[string]string{
		"000001_a.up.sql":   "CREATE TABLE a (id int);",
		"000001_a.down.sql": "DROP TABLE a;",
	}
	good2 = map[string]string{
		"000002_b.up.sql":   "CREATE TABLE b (id int);",
		"000002_b.down.sql": "DROP TABLE b;",
	}
	bad3 = map[string]string{
		"000003_c.up.sql":   "CREATE TABLE c (id int); SELECT 1/0;",
		"000003_c.down.sql": "DROP TABLE c;",
	}
	fixed3 = map[string]string{
		"000003_c.up.sql":   "CREATE TABLE c (id int);",
		"000003_c.down.sql": "DROP TABLE c;",
	}
)

func migrationFS(sets ...map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for _, set := range sets {
		for name, body := range set {
			fsys[name] = &fstest.MapFile{Data: []byte(body)}
		}
	}
	return fsys
}

func up(t *testing.T, dsn string, policy FailurePolicy, sets ...map[string]string) (Result, error) {
	t.Helper()
	return Up(context.Background(), dsn, Options{FS: migrationFS(sets...), OnFailure: policy})
}

func mustUp(t *testing.T, dsn string, sets ...map[string]string) {
	t.Helper()
	if _, err := up(t, dsn, FailureStop, sets...); err != nil {
		t.Fatalf("setup Up: %v", err)
	}
}

// tables lists the user tables, excluding golang-migrate's own.
func tables(t *testing.T, dsn string) string {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// state returns the recorded version ("none" when no row) and dirty flag.
func state(t *testing.T, dsn string) (string, bool) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v sql.NullString
	var dirty bool
	err = db.QueryRow(`SELECT version::text, dirty FROM schema_migrations`).Scan(&v, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return "none", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return v.String, dirty
}

func TestOptionsAreValidatedBeforeConnecting(t *testing.T) {
	ctx := context.Background()
	fsys := migrationFS(good1)
	cases := map[string]Options{
		"no source":      {},
		"both sources":   {FS: fsys, SourceURL: "file://x"},
		"unknown policy": {FS: fsys, OnFailure: FailurePolicy(7)},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Up(ctx, "postgres://invalid:1/none", opts); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestUpAppliesPendingAndIsIdempotent(t *testing.T) {
	dsn := pgtest.NewDatabase(t)

	res, err := up(t, dsn, FailureStop, good1, good2)
	if err != nil {
		t.Fatal(err)
	}
	if res.FromVersion != 0 || res.ToVersion != 2 || !res.Applied() {
		t.Fatalf("first run = %+v", res)
	}
	res, err = up(t, dsn, FailureStop, good1, good2)
	if err != nil {
		t.Fatal(err)
	}
	if res.FromVersion != 2 || res.ToVersion != 2 || res.Applied() {
		t.Fatalf("second run = %+v", res)
	}
	if got := tables(t, dsn); got != "a,b" {
		t.Fatalf("tables = %q", got)
	}
}

func TestStopKeepsEverythingAndLeavesDatabaseDirty(t *testing.T) {
	dsn := pgtest.NewDatabase(t)
	mustUp(t, dsn, good1)

	res, err := up(t, dsn, FailureStop, good1, good2, bad3)
	if err == nil {
		t.Fatal("expected the failing migration to return an error")
	}
	if res.RolledBack || res.FailedVersion != 3 || res.ToVersion != 3 {
		t.Fatalf("result = %+v", res)
	}
	if got := tables(t, dsn); got != "a,b" {
		t.Fatalf("tables = %q, want a,b (no rollback, no partial table c)", got)
	}
	if v, dirty := state(t, dsn); v != "3" || !dirty {
		t.Fatalf("state = %s dirty=%t, want 3 dirty", v, dirty)
	}

	// A dirty database is refused by every policy and left untouched.
	for _, p := range []FailurePolicy{FailureStop, FailureRollbackToPrevious, FailureRollbackToZero} {
		if _, err := up(t, dsn, p, good1, good2, fixed3); !errors.Is(err, ErrDirty) {
			t.Fatalf("policy %s on dirty db: err = %v, want ErrDirty", p, err)
		}
	}
	if got := tables(t, dsn); got != "a,b" {
		t.Fatalf("tables after refused runs = %q", got)
	}
}

func TestRollbackToPreviousRevertsOnlyThisRun(t *testing.T) {
	dsn := pgtest.NewDatabase(t)
	mustUp(t, dsn, good1)

	res, err := up(t, dsn, FailureRollbackToPrevious, good1, good2, bad3)
	if err == nil || !strings.Contains(err.Error(), "rolled back to version 1") {
		t.Fatalf("err = %v, want a failure reporting the rollback", err)
	}
	if !res.RolledBack || res.FromVersion != 1 || res.ToVersion != 1 || res.FailedVersion != 3 {
		t.Fatalf("result = %+v", res)
	}
	if got := tables(t, dsn); got != "a" {
		t.Fatalf("tables = %q, want a", got)
	}
	if v, dirty := state(t, dsn); v != "1" || dirty {
		t.Fatalf("state = %s dirty=%t, want clean 1", v, dirty)
	}

	// The database is clean: once the migration is fixed, it applies.
	res, err = up(t, dsn, FailureRollbackToPrevious, good1, good2, fixed3)
	if err != nil || res.ToVersion != 3 {
		t.Fatalf("after fix: res=%+v err=%v", res, err)
	}
}

func TestRollbackToPreviousFromEmptyDatabase(t *testing.T) {
	dsn := pgtest.NewDatabase(t)

	res, err := up(t, dsn, FailureRollbackToPrevious, good1, good2, bad3)
	if err == nil || !res.RolledBack || res.ToVersion != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got := tables(t, dsn); got != "" {
		t.Fatalf("tables = %q, want none", got)
	}
}

func TestRollbackWhenTheFirstMigrationFails(t *testing.T) {
	dsn := pgtest.NewDatabase(t)
	first := map[string]string{
		"000001_x.up.sql":   "CREATE TABLE x (id int); SELECT 1/0;",
		"000001_x.down.sql": "DROP TABLE x;",
	}

	res, err := up(t, dsn, FailureRollbackToPrevious, first)
	if err == nil || !res.RolledBack || res.ToVersion != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if v, dirty := state(t, dsn); v != "none" || dirty {
		t.Fatalf("state = %s dirty=%t, want no version", v, dirty)
	}
}

func TestRollbackToZeroRevertsEverything(t *testing.T) {
	dsn := pgtest.NewDatabase(t)
	mustUp(t, dsn, good1)

	res, err := up(t, dsn, FailureRollbackToZero, good1, good2, bad3)
	if err == nil || !res.RolledBack || res.ToVersion != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got := tables(t, dsn); got != "" {
		t.Fatalf("tables = %q, want none (rollback to zero)", got)
	}
	if v, dirty := state(t, dsn); v != "none" || dirty {
		t.Fatalf("state = %s dirty=%t, want no version", v, dirty)
	}
}

// Run keeps its signature but no longer rolls back: it used to call
// m.Down(), which dropped every table.
func TestRunNeverRollsBack(t *testing.T) {
	dsn := pgtest.NewDatabase(t)
	dir := t.TempDir()
	for _, set := range []map[string]string{good1, good2, bad3} {
		for name, body := range set {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := Run(dsn, "file://"+filepath.ToSlash(dir)); err == nil {
		t.Fatal("expected an error")
	}
	if got := tables(t, dsn); got != "a,b" {
		t.Fatalf("tables = %q, want a,b", got)
	}
	if v, dirty := state(t, dsn); v != "3" || !dirty {
		t.Fatalf("state = %s dirty=%t, want 3 dirty", v, dirty)
	}
}
