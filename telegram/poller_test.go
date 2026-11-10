package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type scriptedSource struct {
	mu      sync.Mutex
	offsets []int64
	script  []func() ([]Update, error)
	cancel  context.CancelFunc
}

func (s *scriptedSource) GetUpdates(ctx context.Context, p GetUpdatesParams) ([]Update, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets = append(s.offsets, p.Offset)
	if len(s.script) == 0 {
		s.cancel()
		return nil, ctx.Err()
	}
	step := s.script[0]
	s.script = s.script[1:]
	return step()
}

type recordingHandler struct {
	mu   sync.Mutex
	seen []int64
	fail map[int64]string // "error" | "panic"
}

func (r *recordingHandler) Handle(_ context.Context, u Update) error {
	r.mu.Lock()
	r.seen = append(r.seen, u.UpdateID)
	mode := r.fail[u.UpdateID]
	r.mu.Unlock()
	switch mode {
	case "error":
		return errors.New("boom")
	case "panic":
		panic("handler bug")
	}
	return nil
}

func newTestPoller(src UpdateSource, h UpdateHandler) (*Poller, *[]time.Duration) {
	p := NewPoller(src, h, 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var slept []time.Duration
	p.sleep = func(_ context.Context, d time.Duration) { slept = append(slept, d) }
	return p, &slept
}

func ups(ids ...int64) func() ([]Update, error) {
	return func() ([]Update, error) {
		out := make([]Update, len(ids))
		for i, id := range ids {
			out[i] = Update{UpdateID: id}
		}
		return out, nil
	}
}

func fail(err error) func() ([]Update, error) {
	return func() ([]Update, error) { return nil, err }
}

func TestPollerAdvancesOffsetAndSurvivesHandlerFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &scriptedSource{cancel: cancel, script: []func() ([]Update, error){
		ups(100, 101, 102),
		ups(103),
	}}
	h := &recordingHandler{fail: map[int64]string{101: "error", 102: "panic"}}
	p, _ := newTestPoller(src, h)

	if err := p.Run(ctx); err != nil {
		t.Fatalf("graceful shutdown must return nil, got %v", err)
	}
	if want := []int64{100, 101, 102, 103}; !equal(h.seen, want) {
		t.Fatalf("handled %v, want %v", h.seen, want)
	}
	if want := []int64{0, 103, 104}; !equal(src.offsets, want) {
		t.Fatalf("offsets %v, want %v", src.offsets, want)
	}
}

func TestPollerBacksOffAndHonoursRetryAfter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	netErr := errors.New("network unreachable")
	src := &scriptedSource{cancel: cancel, script: []func() ([]Update, error){
		fail(netErr),
		fail(netErr),
		fail(&APIError{Method: "getUpdates", Code: 429, RetryAfter: 20 * time.Second}),
		ups(1),
	}}
	p, slept := newTestPoller(src, &recordingHandler{})

	readyDuringFailures := []error{}
	p.sleep = func(_ context.Context, d time.Duration) {
		*slept = append(*slept, d)
		readyDuringFailures = append(readyDuringFailures, p.Ready())
	}
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 20 * time.Second}; !equal(*slept, want) {
		t.Fatalf("sleeps %v, want %v", *slept, want)
	}
	if readyDuringFailures[0] != nil || readyDuringFailures[1] != nil || readyDuringFailures[2] == nil {
		t.Fatalf("readiness should flip after %d failures: %v", failureThreshold, readyDuringFailures)
	}
}

func TestPollerReadyResetsAfterSuccessAndStopsWhenDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var readyAfterSuccess error = errors.New("unset")
	h := handlerFunc(func(context.Context, Update) error { return nil })
	src := &scriptedSource{cancel: cancel}
	p, _ := newTestPoller(src, h)
	src.script = []func() ([]Update, error){
		fail(errors.New("x")), fail(errors.New("x")), fail(errors.New("x")),
		func() ([]Update, error) { return nil, nil },
		func() ([]Update, error) { readyAfterSuccess = p.Ready(); return nil, nil },
	}
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if readyAfterSuccess != nil {
		t.Fatalf("expected ready after a successful poll, got %v", readyAfterSuccess)
	}
	if p.Ready() == nil {
		t.Fatal("stopped poller must not report ready")
	}
}

func TestBackoffIsCapped(t *testing.T) {
	p, _ := newTestPoller(nil, nil)
	if got := p.backoff(50); got != 30*time.Second {
		t.Fatalf("backoff not capped: %v", got)
	}
}

type handlerFunc func(context.Context, Update) error

func (f handlerFunc) Handle(ctx context.Context, u Update) error { return f(ctx, u) }

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
