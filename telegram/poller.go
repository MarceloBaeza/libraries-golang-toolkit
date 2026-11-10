package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// UpdateSource is the long-polling side of the Bot API.
type UpdateSource interface {
	GetUpdates(ctx context.Context, p GetUpdatesParams) ([]Update, error)
}

// UpdateHandler processes one update.
type UpdateHandler interface {
	Handle(ctx context.Context, u Update) error
}

// Poller runs the getUpdates loop. Only one poller per bot token may run
// (Telegram returns 409 Conflict otherwise): deploy a single replica.
type Poller struct {
	source        UpdateSource
	handler       UpdateHandler
	log           *slog.Logger
	pollTimeout   time.Duration
	handleTimeout time.Duration
	maxBackoff    time.Duration
	sleep         func(context.Context, time.Duration)

	healthy  atomic.Bool
	failures atomic.Int32
}

// NewPoller builds a poller; pollTimeout is the Telegram long-poll timeout.
func NewPoller(source UpdateSource, handler UpdateHandler, pollTimeout time.Duration, log *slog.Logger) *Poller {
	return &Poller{
		source:        source,
		handler:       handler,
		log:           log,
		pollTimeout:   pollTimeout,
		handleTimeout: 30 * time.Second,
		maxBackoff:    30 * time.Second,
		sleep:         sleepCtx,
	}
}

// failureThreshold consecutive poll errors mark the poller as not ready.
const failureThreshold = 3

// Ready reports whether polling is currently working.
func (p *Poller) Ready() error {
	if !p.healthy.Load() {
		return fmt.Errorf("telegram poller not ready (consecutive failures: %d)", p.failures.Load())
	}
	return nil
}

// Run polls until ctx is cancelled. It returns nil on graceful shutdown.
// Updates are processed sequentially; the offset is advanced after each
// update is handled, so a crash causes at-most a re-delivery (at least once).
func (p *Poller) Run(ctx context.Context) error {
	var offset int64
	p.healthy.Store(true)
	defer p.healthy.Store(false)

	for ctx.Err() == nil {
		pollCtx, cancel := context.WithTimeout(ctx, p.pollTimeout+15*time.Second)
		updates, err := p.source.GetUpdates(pollCtx, GetUpdatesParams{
			Offset:         offset,
			Timeout:        int(p.pollTimeout / time.Second),
			AllowedUpdates: []string{"message", "callback_query"},
		})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			n := p.failures.Add(1)
			if n >= failureThreshold {
				p.healthy.Store(false)
			}
			delay := p.backoff(int(n))
			if retry, ok := IsRateLimited(err); ok && retry > delay {
				delay = retry
			}
			p.log.Warn("telegram: getUpdates failed", slog.Any("error", err),
				slog.Int("consecutive_failures", int(n)), slog.Duration("retry_in", delay))
			p.sleep(ctx, delay)
			continue
		}
		p.failures.Store(0)
		p.healthy.Store(true)

		for _, u := range updates {
			p.dispatch(ctx, u)
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
		}
	}
	return nil
}

func (p *Poller) dispatch(ctx context.Context, u Update) {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.handleTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("telegram: panic while handling update", slog.Int64("update_id", u.UpdateID), slog.Any("panic", r))
		}
	}()
	if err := p.handler.Handle(hctx, u); err != nil {
		p.log.Error("telegram: handle update failed", slog.Int64("update_id", u.UpdateID), slog.Any("error", err))
	}
}

func (p *Poller) backoff(failures int) time.Duration {
	d := time.Second
	for i := 1; i < failures && d < p.maxBackoff; i++ {
		d *= 2
	}
	return min(d, p.maxBackoff)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
