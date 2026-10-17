// Package pgtest creates disposable PostgreSQL databases for integration
// tests. Tests are skipped unless TOOLKIT_TEST_DATABASE_URL points to a
// server where the user may CREATE DATABASE. Never point it at production.
//
// It is imported only by _test.go files, so the public packages stay
// independent of each other.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnvVar is the admin DSN used to create throwaway databases.
const EnvVar = "TOOLKIT_TEST_DATABASE_URL"

// NewDatabase creates an empty database and returns its DSN. The database
// is dropped when the test ends.
func NewDatabase(t testing.TB) string {
	t.Helper()
	admin := os.Getenv(EnvVar)
	if admin == "" {
		t.Skipf("%s not set; skipping PostgreSQL integration test", EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("pgtest: connect admin: %v", err)
	}
	defer conn.Close(ctx)

	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	name := "tktest_" + hex.EncodeToString(buf)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Logf("pgtest: cleanup connect: %v", err)
			return
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Logf("pgtest: drop %s: %v", name, err)
		}
	})

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("pgtest: %s must be a URL DSN: %v", EnvVar, err)
	}
	u.Path = "/" + name
	return u.String()
}
