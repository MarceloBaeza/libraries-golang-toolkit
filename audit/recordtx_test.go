package audit

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MarceloBaeza/libraries-golang-toolkit/internal/pgtest"
)

// auditTable is the column contract documented in the package comment.
const auditTable = `CREATE TABLE audit_logs (
	id bigserial PRIMARY KEY,
	user_id text, module text NOT NULL, action text NOT NULL,
	entity_type text NOT NULL, entity_id text, result text NOT NULL,
	old_value jsonb, new_value jsonb, ip_address inet, user_agent text)`

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.NewDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, auditTable); err != nil {
		t.Fatal(err)
	}
	return pool
}

func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordTxFollowsTheTransaction(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	entry := Entry{UserID: "u1", Module: "finance", Action: "create", EntityType: "movement", EntityID: "m1",
		NewValue: map[string]any{"fields": []string{"amount"}}}

	errAbort := errors.New("business rule failed")
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := RecordTx(ctx, tx, entry); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("err = %v", err)
	}
	if n := countRows(t, pool); n != 0 {
		t.Fatalf("rows after rollback = %d, want 0", n)
	}

	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return RecordTx(ctx, tx, entry) }); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool); n != 1 {
		t.Fatalf("rows after commit = %d, want 1", n)
	}
}

// RecordTx and Record must write identical rows.
func TestRecordTxWritesTheSameRowAsRecord(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	entry := Entry{Module: "finance", Action: "update", EntityType: "movement", EntityID: "m1",
		Result: ResultDenied, OldValue: map[string]any{"a": 1}, IPAddress: "10.0.0.1", UserAgent: "test"}

	if err := New(pool).Record(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := RecordTx(ctx, pool, entry); err != nil {
		t.Fatal(err)
	}
	var distinct int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT DISTINCT user_id, module, action, entity_type, entity_id, result,
			old_value, new_value, ip_address, user_agent FROM audit_logs) d`).Scan(&distinct)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool); n != 2 || distinct != 1 {
		t.Fatalf("rows=%d distinct=%d, want 2 identical rows", n, distinct)
	}
}

func TestRecordTxRejectsIncompleteEntries(t *testing.T) {
	if err := RecordTx(context.Background(), nil, Entry{Module: "finance"}); err == nil {
		t.Fatal("expected an error before touching the database")
	}
}
