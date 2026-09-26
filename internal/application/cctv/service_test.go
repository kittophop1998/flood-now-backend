package cctv_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appcctv "floodnow-api/internal/application/cctv"
	"floodnow-api/internal/domain/apperr"
	domain "floodnow-api/internal/domain/cctv"
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
	gate  chan struct{}
}

func cam(id string, lat, lng float64) domain.Camera {
	c, err := domain.NewDOHCamera(id, "PER-"+id, lat, lng, "1", "0401", "10+000")
	if err != nil {
		panic(err)
	}
	return c
}

func (p *fakeProvider) Cameras(ctx context.Context) (*domain.Catalog, error) {
	p.calls.Add(1)
	if p.gate != nil {
		<-p.gate
	}
	if p.fail.Load() {
		return nil, apperr.Unavailable("down")
	}
	return &domain.Catalog{
		Cameras: []domain.Camera{
			cam("1", 13.7563, 100.5018), // Bangkok
			cam("2", 13.7600, 100.5018), // ~410 m north
			cam("3", 18.7883, 98.9853),  // Chiang Mai
		},
		FetchedAt: p.clock.Now(),
	}, nil
}

func setup() (*appcctv.Service, *fakeProvider, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)}
	p := &fakeProvider{clock: clock}
	return appcctv.NewService(p, clock, time.Hour), p, clock
}

func code(err error) apperr.Code {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestCacheMissThenHit(t *testing.T) {
	s, p, clock := setup()
	ctx := context.Background()
	res, err := s.List(ctx, nil, 0)
	if err != nil || len(res.Cameras) != 3 || res.Stale || res.HasMore {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	// Different viewports, nearby lookups and gets all share one fetch.
	clock.advance(50 * time.Minute)
	s.List(ctx, &domain.Bounds{MinLng: 100, MinLat: 13, MaxLng: 101, MaxLat: 14}, 0)
	s.Nearby(ctx, appcctv.NearbyInput{Latitude: 13.75, Longitude: 100.5})
	s.Get(ctx, "doh-1")
	if p.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (cache hit)", p.calls.Load())
	}
	clock.advance(11 * time.Minute) // past the TTL
	if _, err := s.List(ctx, nil, 0); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (refetch after TTL)", p.calls.Load())
	}
}

func TestBBoxFilteringAndLimit(t *testing.T) {
	s, _, _ := setup()
	ctx := context.Background()
	res, _ := s.List(ctx, &domain.Bounds{MinLng: 100, MinLat: 13, MaxLng: 101, MaxLat: 14}, 0)
	if len(res.Cameras) != 2 || res.Cameras[0].ID != "doh-1" || res.Cameras[1].ID != "doh-2" {
		t.Fatalf("bbox result = %+v", res.Cameras)
	}
	res, _ = s.List(ctx, nil, 2)
	if len(res.Cameras) != 2 || !res.HasMore {
		t.Fatalf("limited result = %+v more=%v", res.Cameras, res.HasMore)
	}
	res, _ = s.List(ctx, &domain.Bounds{MinLng: 0, MinLat: 0, MaxLng: 1, MaxLat: 1}, 0)
	if res.Cameras == nil || len(res.Cameras) != 0 {
		t.Fatalf("empty viewport must be an empty list, got %#v", res.Cameras)
	}
}

func TestNearby(t *testing.T) {
	s, _, _ := setup()
	ctx := context.Background()
	res, err := s.Nearby(ctx, appcctv.NearbyInput{Latitude: 13.7563, Longitude: 100.5018})
	if err != nil || len(res.Cameras) != 2 || res.Cameras[0].ID != "doh-1" || res.Cameras[1].DistanceM < 400 || res.Cameras[1].DistanceM > 420 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	res, _ = s.Nearby(ctx, appcctv.NearbyInput{Latitude: 13.7563, Longitude: 100.5018, RadiusM: 100})
	if len(res.Cameras) != 1 {
		t.Fatalf("100 m radius = %+v", res.Cameras)
	}
	res, _ = s.Nearby(ctx, appcctv.NearbyInput{Latitude: 13.7563, Longitude: 100.5018, Limit: 1})
	if len(res.Cameras) != 1 {
		t.Fatalf("limit 1 = %+v", res.Cameras)
	}
	for _, in := range []appcctv.NearbyInput{{Latitude: 91}, {Longitude: 200}, {RadiusM: 6000}, {Limit: 11}, {RadiusM: -1}} {
		if _, err := s.Nearby(ctx, in); code(err) != apperr.CodeValidation {
			t.Errorf("%+v: err = %v, want VALIDATION_ERROR", in, err)
		}
	}
}

func TestGet(t *testing.T) {
	s, _, _ := setup()
	res, err := s.Get(context.Background(), "doh-3")
	if err != nil || res.Camera.Name != "PER-3" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := s.Get(context.Background(), "doh-999"); code(err) != apperr.CodeNotFound {
		t.Fatalf("err = %v, want NOT_FOUND", err)
	}
}

func TestProviderFailureStaleFallbackAndBackoff(t *testing.T) {
	s, p, clock := setup()
	ctx := context.Background()
	first, _ := s.List(ctx, nil, 0)

	p.fail.Store(true)
	clock.advance(2 * time.Hour) // past TTL
	res, err := s.List(ctx, nil, 0)
	if err != nil || !res.Stale || !res.FetchedAt.Equal(first.FetchedAt) || len(res.Cameras) != 3 {
		t.Fatalf("want stale copy, got res=%+v err=%v", res, err)
	}
	// Within the backoff no new upstream call is made (no retry storm).
	for range 5 {
		if res, err := s.List(ctx, nil, 0); err != nil || !res.Stale {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	}
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one failed refresh, then backoff)", p.calls.Load())
	}
	// After the backoff it tries again; once the provider is back it's fresh.
	p.fail.Store(false)
	clock.advance(2 * time.Minute)
	res, err = s.List(ctx, nil, 0)
	if err != nil || res.Stale || p.calls.Load() != 3 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, p.calls.Load())
	}
	// A copy older than 24 h is never served, even as stale.
	p.fail.Store(true)
	clock.advance(25 * time.Hour)
	if _, err := s.List(ctx, nil, 0); code(err) != apperr.CodeUnavailable {
		t.Fatalf("err = %v, want UNAVAILABLE", err)
	}
}

func TestFailureWithoutCopyIsUnavailable(t *testing.T) {
	s, p, _ := setup()
	p.fail.Store(true)
	if _, err := s.List(context.Background(), nil, 0); code(err) != apperr.CodeUnavailable {
		t.Fatalf("err = %v, want UNAVAILABLE", err)
	}
}

func TestConcurrentMissesShareOneFetch(t *testing.T) {
	s, p, _ := setup()
	p.gate = make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.List(context.Background(), nil, 0); err != nil {
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
