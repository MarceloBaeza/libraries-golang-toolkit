package flowpg

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MarceloBaeza/libraries-golang-toolkit/internal/pgtest"
	"github.com/MarceloBaeza/libraries-golang-toolkit/telegram"
)

const ddl = `CREATE TABLE telegram_flow_sessions (
	user_id uuid NOT NULL, chat_id bigint NOT NULL,
	flow_id uuid NOT NULL DEFAULT gen_random_uuid(),
	flow_type text NOT NULL, step text NOT NULL,
	revision integer NOT NULL DEFAULT 0,
	state jsonb NOT NULL DEFAULT '{}'::jsonb,
	expires_at timestamptz NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (user_id, chat_id), UNIQUE (flow_id))`

const user = "11111111-2222-3333-4444-555555555555"

func newStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.NewDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	return New(pool)
}

func TestStoreLifecycle(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	exp := now.Add(time.Hour)

	if _, err := s.Get(ctx, user, 1, now); !errors.Is(err, telegram.ErrNoSession) {
		t.Fatalf("expected no session, got %v", err)
	}
	a, err := s.Start(ctx, user, 1, "fin.ingreso", "fondo", json.RawMessage(`{"a":1}`), exp)
	if err != nil || a.Revision != 0 {
		t.Fatalf("start: %+v %v", a, err)
	}
	b, err := s.Advance(ctx, user, 1, a.FlowID, 0, "monto", json.RawMessage(`{"a":2}`), now, exp)
	if err != nil || b.Revision != 1 || b.Step != "monto" {
		t.Fatalf("advance: %+v %v", b, err)
	}
	// Same revision again (double press) is stale.
	if _, err := s.Advance(ctx, user, 1, a.FlowID, 0, "x", nil, now, exp); !errors.Is(err, telegram.ErrStale) {
		t.Fatalf("expected stale, got %v", err)
	}
	// Restarting replaces the flow id: old buttons die.
	c, err := s.Start(ctx, user, 1, "fin.egreso", "fondo", nil, exp)
	if err != nil || c.FlowID == a.FlowID || c.Revision != 0 {
		t.Fatalf("restart: %+v %v", c, err)
	}
	if _, err := s.Advance(ctx, user, 1, a.FlowID, 1, "x", nil, now, exp); !errors.Is(err, telegram.ErrStale) {
		t.Fatalf("old flow must be stale, got %v", err)
	}
	// Expired sessions are invisible and cannot advance.
	later := exp.Add(time.Minute)
	if _, err := s.Get(ctx, user, 1, later); !errors.Is(err, telegram.ErrNoSession) {
		t.Fatalf("expired must be absent, got %v", err)
	}
	if _, err := s.Advance(ctx, user, 1, c.FlowID, 0, "x", nil, later, later.Add(time.Hour)); !errors.Is(err, telegram.ErrStale) {
		t.Fatalf("expired must be stale, got %v", err)
	}
	if n, err := s.DeleteExpired(ctx, later); err != nil || n != 1 {
		t.Fatalf("delete expired: %d %v", n, err)
	}
	// End is idempotent and ignores malformed ids.
	if err := s.End(ctx, user, 1, "not-a-uuid"); err != nil {
		t.Fatal(err)
	}
	if err := s.End(ctx, user, 1, c.FlowID); err != nil {
		t.Fatal(err)
	}
}
