package officialflood_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appflood "floodnow-api/internal/application/officialflood"
	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/officialflood"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeProvider struct {
	clock *fakeClock
	calls atomic.Int32
	fail  atomic.Bool
	gate  chan struct{} // when set, each call waits for it
}

func square(lng, lat float64) domain.Area {
	a, err := domain.NewArea("", []domain.Polygon{{{{lng, lat}, {lng + 0.1, lat}, {lng + 0.1, lat + 0.1}, {lng, lat + 0.1}, {lng, lat}}}}, nil)
	if err != nil {
		panic(err)
	}
	return a
}

func (p *fakeProvider) FloodAreas(ctx context.Context, period domain.Period) (*domain.Snapshot, error) {
	p.calls.Add(1)
	if p.gate != nil {
		<-p.gate
	}
	if p.fail.Load() {
		return nil, apperr.Unavailable("down")
	}
	return domain.NewSnapshot(period, []domain.Area{square(100.5, 13.7), square(102, 16)}, p.clock.Now()), nil
}

func setup() (*appflood.Service, *fakeProvider, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)}
	p := &fakeProvider{clock: clock}
	return appflood.NewService(p, clock, 15*time.Minute), p, clock
}

func TestCacheMissThenHit(t *testing.T) {
	s, p, clock := setup()
	ctx := context.Background()
	l, err := s.Get(ctx, domain.Period1D, nil)
	if err != nil || len(l.Areas) != 2 || l.Stale {
		t.Fatalf("layer=%+v err=%v", l, err)
	}
	clock.advance(10 * time.Minute)
	if _, err := s.Get(ctx, domain.Period1D, nil); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (cache hit)", p.calls.Load())
	}
	// Periods are cached separately.
	if _, err := s.Get(ctx, domain.Period7D, nil); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", p.calls.Load())
	}
	// Past the TTL the period is fetched again.
	clock.advance(6 * time.Minute)
	if _, err := s.Get(ctx, domain.Period1D, nil); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (refetch after TTL)", p.calls.Load())
	}
}

func TestViewportClipping(t *testing.T) {
	s, _, _ := setup()
	view := &domain.Bounds{MinLng: 100, MinLat: 13, MaxLng: 101, MaxLat: 14}
	l, err := s.Get(context.Background(), domain.Period1D, view)
	if err != nil || len(l.Areas) != 1 || l.Areas[0].Ref != 0 {
		t.Fatalf("layer=%+v err=%v", l, err)
	}
}

func TestStaleFallbackAndBackoff(t *testing.T) {
	s, p, clock := setup()
	ctx := context.Background()
	first, _ := s.Get(ctx, domain.Period1D, nil)

	p.fail.Store(true)
	clock.advance(20 * time.Minute) // past TTL
	l, err := s.Get(ctx, domain.Period1D, nil)
	if err != nil || !l.Stale || !l.FetchedAt.Equal(first.FetchedAt) || len(l.Areas) != 2 {
		t.Fatalf("want stale copy, got layer=%+v err=%v", l, err)
	}
	// Within the backoff no new upstream call is made (no retry storm).
	for i := 0; i < 5; i++ {
		if l, err := s.Get(ctx, domain.Period1D, nil); err != nil || !l.Stale {
			t.Fatalf("layer=%+v err=%v", l, err)
		}
	}
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", p.calls.Load())
	}
	// After the backoff it tries again and recovers.
	p.fail.Store(false)
	clock.advance(2 * time.Minute)
	l, err = s.Get(ctx, domain.Period1D, nil)
	if err != nil || l.Stale || p.calls.Load() != 3 {
		t.Fatalf("layer=%+v err=%v calls=%d", l, err, p.calls.Load())
	}
}

func TestNoCopyMeansUnavailable(t *testing.T) {
	s, p, _ := setup()
	p.fail.Store(true)
	_, err := s.Get(context.Background(), domain.Period1D, nil)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.CodeUnavailable {
		t.Fatalf("err = %v", err)
	}
	// And it isn't hammered while backing off.
	_, _ = s.Get(context.Background(), domain.Period1D, nil)
	if p.calls.Load() != 1 {
		t.Fatalf("calls = %d", p.calls.Load())
	}
}

func TestStaleCopyExpires(t *testing.T) {
	s, p, clock := setup()
	_, _ = s.Get(context.Background(), domain.Period1D, nil)
	p.fail.Store(true)
	clock.advance(25 * time.Hour)
	if _, err := s.Get(context.Background(), domain.Period1D, nil); err == nil {
		t.Fatal("a day-old copy must not be served")
	}
}

func TestConcurrentMissesShareOneFetch(t *testing.T) {
	s, p, _ := setup()
	p.gate = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Get(context.Background(), domain.Period1D, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(p.gate)
	wg.Wait()
	if p.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", p.calls.Load())
	}
}

func TestRequestDeadlineFallsBackWithoutCancellingFetch(t *testing.T) {
	s, p, clock := setup()
	_, _ = s.Get(context.Background(), domain.Period1D, nil)
	clock.advance(20 * time.Minute)
	p.gate = make(chan struct{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	l, err := s.Get(ctx, domain.Period1D, nil)
	if err != nil || !l.Stale {
		t.Fatalf("slow provider: want stale copy, got layer=%+v err=%v", l, err)
	}
	close(p.gate) // the shared refresh completes on its own
	l, err = s.Get(context.Background(), domain.Period1D, nil)
	if err != nil || l.Stale {
		t.Fatalf("layer=%+v err=%v", l, err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d", p.calls.Load())
	}
}

func TestInvalidPeriod(t *testing.T) {
	s, _, _ := setup()
	if _, err := s.Get(context.Background(), domain.Period("2d"), nil); err == nil {
		t.Fatal("want validation error")
	}
}
