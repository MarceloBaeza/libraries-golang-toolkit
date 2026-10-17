// Package audit records who did what, to what, and with what result, as a
// row in an existing "audit_logs" table.
//
// It does not create or migrate that table — the caller must already have
// one with these columns, in this order: user_id (nullable), module,
// action, entity_type, entity_id (nullable), result, old_value (jsonb,
// nullable), new_value (jsonb, nullable), ip_address (inet, nullable),
// user_agent (nullable).
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Result is the outcome of the audited action.
type Result string

const (
	// ResultSuccess is also what a zero-value Entry.Result records: most
	// callers only audit actions that already succeeded, so requiring it
	// on every call site would just be repetition.
	ResultSuccess Result = "success"
	ResultDenied  Result = "denied"
	ResultFailed  Result = "failed"
)

// Entry is one audit record. UserID, EntityID, IPAddress and UserAgent are
// stored as SQL NULL when left empty, rather than as an empty string.
type Entry struct {
	UserID     string
	Module     string
	Action     string
	EntityType string
	EntityID   string
	Result     Result
	OldValue   map[string]any
	NewValue   map[string]any
	IPAddress  string
	UserAgent  string
}

// Recorder writes Entry values to the audit_logs table through an existing
// pool. It holds no other state, so callers own the pool's lifecycle —
// build it once with dbpool.New (or any *pgxpool.Pool) and share it.
type Recorder struct {
	pool *pgxpool.Pool
}

// New wraps an already-connected pool. It does not open or verify a
// connection itself.
func New(pool *pgxpool.Pool) *Recorder {
	return &Recorder{pool: pool}
}

const insertEntrySQL = `
	INSERT INTO audit_logs (user_id, module, action, entity_type, entity_id, result, old_value, new_value, ip_address, user_agent)
	VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::inet, $10)`

// Record inserts e as a new audit row. The returned error is never
// swallowed internally — callers that want the "audit everything"
// guarantee to actually hold must check it (log it, retry it, whatever
// fits), because a discarded error here is a silent gap in the trail.
func (r *Recorder) Record(ctx context.Context, e Entry) error {
	args, err := argsFor(e)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := r.pool.Exec(ctx, insertEntrySQL, args...); err != nil {
		return fmt.Errorf("audit: record entry: %w", err)
	}
	return nil
}

// Execer is satisfied by pgx.Tx, *pgx.Conn and *pgxpool.Pool.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RecordTx inserts e using db, normally the caller's transaction, so the
// audit row commits or rolls back together with the audited change.
// Record, by contrast, writes through the pool outside any transaction: a
// change could commit while its audit row is lost, or the reverse.
//
// Unlike Record it adds no timeout of its own; the transaction's context
// governs it. As with Record, the returned error must be checked —
// returning it from the transaction function aborts the change too.
func RecordTx(ctx context.Context, db Execer, e Entry) error {
	args, err := argsFor(e)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, insertEntrySQL, args...); err != nil {
		return fmt.Errorf("audit: record entry: %w", err)
	}
	return nil
}

// argsFor builds the positional SQL arguments for insertEntrySQL, in
// column order. Kept separate from Record so the mapping (defaulting,
// nullability, JSON marshaling) is unit-testable without a database.
func argsFor(e Entry) ([]any, error) {
	if e.Module == "" || e.Action == "" || e.EntityType == "" {
		return nil, errors.New("audit: entry requires module, action and entity type")
	}

	result := e.Result
	if result == "" {
		result = ResultSuccess
	}

	oldValue, err := marshalNullable(e.OldValue)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal old value: %w", err)
	}
	newValue, err := marshalNullable(e.NewValue)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal new value: %w", err)
	}

	return []any{
		nullable(e.UserID), e.Module, e.Action, e.EntityType, nullable(e.EntityID),
		string(result), oldValue, newValue, nullable(e.IPAddress), nullable(e.UserAgent),
	}, nil
}

// nullable turns an empty string into a SQL NULL parameter instead of an
// empty-string value.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// marshalNullable JSON-encodes v, or returns nil (SQL NULL) for a nil map.
// Unlike a discarded "_, _ = json.Marshal(...)", a value that fails to
// encode is surfaced as an error instead of silently becoming NULL.
func marshalNullable(v map[string]any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
