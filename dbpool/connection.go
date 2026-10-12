package dbpool

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Settings holds the connection-pool tuning knobs. Zero values fall back to
// pgxpool defaults, except StatementCacheCap which defaults to 256.
type Settings struct {
	DSN                string
	MaxOpenConnections int
	MinOpenConnections int
	MaxConnLifetime    int // seconds
	MaxConnIdleTime    int // seconds
	HealthCheckPeriod  int // seconds
	StatementCacheCap  int
}

// New creates and pings a pgx connection pool. Callers own the pool's
// lifecycle (memoization, Close, etc.) — this package holds no package-level
// state, so it's safe to create multiple independent pools per process.
func New(ctx context.Context, s Settings) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(s.DSN)
	if err != nil {
		return nil, fmt.Errorf("dbpool: parse config: %w", err)
	}
	cfg.MaxConns = int32(s.MaxOpenConnections)
	cfg.MinConns = int32(s.MinOpenConnections)
	cfg.MaxConnLifetime = time.Duration(s.MaxConnLifetime) * time.Second
	cfg.MaxConnIdleTime = time.Duration(s.MaxConnIdleTime) * time.Second
	cfg.HealthCheckPeriod = time.Duration(s.HealthCheckPeriod) * time.Second

	cacheCap := s.StatementCacheCap
	if cacheCap <= 0 {
		cacheCap = 256
	}
	cfg.ConnConfig.StatementCacheCapacity = cacheCap

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("dbpool: create pool: %w", err)
	}

	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("dbpool: ping: %w", err)
	}
	return pool, nil
}
