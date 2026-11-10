// Package flowpg implements telegram.FlowStore on PostgreSQL.
//
// The caller provisions the table (this package does not migrate). The
// user_id column may be uuid, text or bigint: values are passed as
// parameters and PostgreSQL converts them to the column type.
//
//	CREATE TABLE telegram_flow_sessions (
//	    user_id    uuid        NOT NULL,            -- or text / bigint
//	    chat_id    bigint      NOT NULL,
//	    flow_id    uuid        NOT NULL DEFAULT gen_random_uuid(),
//	    flow_type  text        NOT NULL CHECK (length(btrim(flow_type)) > 0),
//	    step       text        NOT NULL CHECK (length(btrim(step)) > 0),
//	    revision   integer     NOT NULL DEFAULT 0 CHECK (revision >= 0),
//	    state      jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(state) = 'object'),
//	    expires_at timestamptz NOT NULL,
//	    created_at timestamptz NOT NULL DEFAULT now(),
//	    updated_at timestamptz NOT NULL DEFAULT now(),
//	    PRIMARY KEY (user_id, chat_id),
//	    UNIQUE (flow_id)
//	);
//	CREATE INDEX idx_telegram_flow_sessions_expiry ON telegram_flow_sessions (expires_at);
//
// gen_random_uuid() is built in since PostgreSQL 13. Expired rows are
// ignored by every query; DeleteExpired removes them.
package flowpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MarceloBaeza/libraries-golang-toolkit/telegram"
)

// Store implements telegram.FlowStore.
type Store struct {
	pool  *pgxpool.Pool
	table string
}

// New wraps an existing pool; the caller owns its lifecycle. The table is
// telegram_flow_sessions.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool, table: "telegram_flow_sessions"} }

var _ telegram.FlowStore = (*Store)(nil)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const columns = `flow_id::text, flow_type, step, revision, state, expires_at`

func scan(row pgx.Row) (*telegram.Session, error) {
	var s telegram.Session
	var state []byte
	if err := row.Scan(&s.FlowID, &s.Type, &s.Step, &s.Revision, &state, &s.ExpiresAt); err != nil {
		return nil, err
	}
	s.State = json.RawMessage(state)
	return &s, nil
}

func stateArg(state json.RawMessage) string {
	if len(state) == 0 {
		return "{}"
	}
	return string(state)
}

// Get implements telegram.FlowStore.
func (s *Store) Get(ctx context.Context, userKey string, chatID int64, now time.Time) (*telegram.Session, error) {
	sess, err := scan(s.pool.QueryRow(ctx, `SELECT `+columns+` FROM `+s.table+`
		WHERE user_id = $1 AND chat_id = $2 AND expires_at > $3`, userKey, chatID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, telegram.ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("flowpg: get: %w", err)
	}
	return sess, nil
}

// Start implements telegram.FlowStore. A new flow_id is generated even when
// a row for the same (user, chat) exists, so buttons of the replaced flow die.
func (s *Store) Start(ctx context.Context, userKey string, chatID int64, flowType, step string, state json.RawMessage, expiresAt time.Time) (*telegram.Session, error) {
	sess, err := scan(s.pool.QueryRow(ctx, `
		INSERT INTO `+s.table+` (user_id, chat_id, flow_type, step, state, expires_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (user_id, chat_id) DO UPDATE SET
			flow_id = gen_random_uuid(), flow_type = EXCLUDED.flow_type, step = EXCLUDED.step,
			revision = 0, state = EXCLUDED.state, expires_at = EXCLUDED.expires_at, updated_at = now()
		RETURNING `+columns, userKey, chatID, flowType, step, stateArg(state), expiresAt))
	if err != nil {
		return nil, fmt.Errorf("flowpg: start: %w", err)
	}
	return sess, nil
}

// Advance implements telegram.FlowStore.
func (s *Store) Advance(ctx context.Context, userKey string, chatID int64, flowID string, revision int, step string, state json.RawMessage, now, expiresAt time.Time) (*telegram.Session, error) {
	if !uuidRe.MatchString(flowID) {
		return nil, telegram.ErrStale
	}
	sess, err := scan(s.pool.QueryRow(ctx, `
		UPDATE `+s.table+`
		SET step = $5, state = $6::jsonb, revision = revision + 1, expires_at = $8, updated_at = now()
		WHERE user_id = $1 AND chat_id = $2 AND flow_id = $3::uuid AND revision = $4 AND expires_at > $7
		RETURNING `+columns, userKey, chatID, flowID, revision, step, stateArg(state), now, expiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, telegram.ErrStale
	}
	if err != nil {
		return nil, fmt.Errorf("flowpg: advance: %w", err)
	}
	return sess, nil
}

// End implements telegram.FlowStore.
func (s *Store) End(ctx context.Context, userKey string, chatID int64, flowID string) error {
	if !uuidRe.MatchString(flowID) {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM `+s.table+`
		WHERE user_id = $1 AND chat_id = $2 AND flow_id = $3::uuid`, userKey, chatID, flowID); err != nil {
		return fmt.Errorf("flowpg: end: %w", err)
	}
	return nil
}

// DeleteExpired removes sessions that expired before now and returns how
// many were deleted. Optional housekeeping.
func (s *Store) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM `+s.table+` WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("flowpg: delete expired: %w", err)
	}
	return tag.RowsAffected(), nil
}
