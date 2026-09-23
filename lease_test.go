package coldwire

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type leaseApp struct {
	Graph
	DB, Handler Provider[string, Deferred]
	log         []string
	mu          sync.Mutex
}

func newLeaseApp() *leaseApp {
	a := &leaseApp{}
	closeAs := func(s Deferred, name string) {
		s.OnClose(func() error {
			a.mu.Lock()
			a.log = append(a.log, "close "+name)
			a.mu.Unlock()
			return nil
		})
	}
	a.DB = a.Lazily(func(s Deferred) string { closeAs(s, "db"); return "db" })
	a.Handler = a.Lazily(func(s Deferred) string { closeAs(s, "handler"); return "h(" + a.DB.From(s) + ")" })
	return a
}

func (a *leaseApp) closed() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.log, ",")
}

// A request holding the handler keeps it, and the database it was built
// from, open across an invalidation; both close when the request ends.
func TestLeaseSurvivesInvalidate(t *testing.T) {
	a := newLeaseApp()
	h, lease, err := a.Handler.Acquire(t.Context())
	if err != nil || h != "h(db)" {
		t.Fatal(h, err)
	}
	if err := a.Invalidate(a.DB); err != nil {
		t.Fatal(err)
	}
	if got := a.closed(); got != "" {
		t.Fatalf("closed under a live lease: %s", got)
	}
	// New requests get fresh values meanwhile.
	h2, lease2, _ := a.Handler.Acquire(t.Context())
	if h2 != "h(db)" {
		t.Fatal(h2)
	}
	lease.Release()
	lease.Release() // extra calls do nothing
	if got := a.closed(); got != "close handler,close db" {
		t.Fatalf("after release: %s", got)
	}
	lease2.Release()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if got := a.closed(); got != "close handler,close db,close handler,close db" {
		t.Fatalf("after Close: %s", got)
	}
}

// Close waits for leases to be released.
func TestCloseWaitsForLeases(t *testing.T) {
	a := newLeaseApp()
	_, lease, _ := a.Handler.Acquire(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Close() }()
	select {
	case <-done:
		t.Fatal("Close returned while a lease was held")
	case <-time.After(20 * time.Millisecond):
	}
	if got := a.closed(); got != "" {
		t.Fatalf("closed under a live lease: %s", got)
	}
	lease.Release()
	within(t, time.Second, func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	if got := a.closed(); got != "close handler,close db" {
		t.Fatalf("got %s", got)
	}
}

// At its deadline CloseContext cleans up anyway and names what it could not
// wait for: the leased values and the builder ignoring its context.
func TestCloseContextReportsStragglers(t *testing.T) {
	a := newLeaseApp()
	_, lease, _ := a.Handler.Acquire(t.Context())
	defer lease.Release()
	stuck := make(chan struct{})
	defer close(stuck)
	started := make(chan struct{})
	slow := a.Lazily(func(Deferred) int { close(started); <-stuck; return 0 }) // ignores s.Context()
	go slow.TryContext(t.Context())
	<-started

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := a.CloseContext(ctx)
	var ce *CloseError
	if !errors.As(err, &ce) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if len(ce.Building) != 1 || !strings.HasPrefix(ce.Building[0], "coldwire/lease_test.go:") {
		t.Fatalf("building = %v", ce.Building)
	}
	if len(ce.Leased) != 2 {
		t.Fatalf("leased = %v", ce.Leased)
	}
	if got := a.closed(); got != "close handler,close db" && got != "close db,close handler" {
		t.Fatalf("not cleaned up anyway: %s", got)
	}
}

// Close waits for a build in progress that honours its context.
func TestCloseWaitsForBuildsInProgress(t *testing.T) {
	g := new(Graph)
	started := make(chan struct{})
	var cleaned bool
	p := g.TryLazily(func(s Deferred) (int, error) {
		s.OnClose(func() error { cleaned = true; return nil })
		close(started)
		<-s.Context().Done()
		time.Sleep(10 * time.Millisecond) // shutting down takes a moment
		return 0, s.Context().Err()
	})
	go p.TryContext(t.Context())
	<-started
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if !cleaned {
		t.Fatal("Close returned before the build released its resources")
	}
}

// Release errors that happen after Invalidate are reported by Close.
func TestLateCleanupErrorsReachClose(t *testing.T) {
	g := new(Graph)
	p := g.Lazily(func(s Deferred) int { s.OnClose(func() error { return errBoom }); return 1 })
	_, lease, _ := p.Acquire(t.Context())
	if err := g.Invalidate(p); err != nil {
		t.Fatal(err) // still leased: nothing cleaned up yet
	}
	lease.Release()
	if err := g.Close(); !errors.Is(err, errBoom) {
		t.Fatalf("Close = %v", err)
	}
}

// Leases, invalidations and Close racing: -race stays quiet, every cleanup
// runs exactly once, and never while its value is leased.
func TestLeaseStress(t *testing.T) {
	a := &leaseApp{}
	var mu sync.Mutex
	open := map[int]int{} // value id -> leases
	closedIDs := map[int]int{}
	next := 0
	a.DB = a.Lazily(func(s Deferred) string {
		mu.Lock()
		next++
		id := next
		mu.Unlock()
		s.OnClose(func() error {
			mu.Lock()
			defer mu.Unlock()
			if open[id] != 0 {
				t.Errorf("value %d closed with %d leases", id, open[id])
			}
			closedIDs[id]++
			return nil
		})
		return strings.Repeat("x", id)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for ctx.Err() == nil {
				v, lease, err := a.DB.Acquire(ctx)
				if err != nil {
					continue
				}
				id := len(v)
				mu.Lock()
				open[id]++
				mu.Unlock()
				time.Sleep(time.Microsecond)
				mu.Lock()
				open[id]--
				mu.Unlock()
				lease.Release()
			}
		})
	}
	wg.Go(func() {
		for ctx.Err() == nil {
			a.Invalidate(a.DB)
			time.Sleep(100 * time.Microsecond)
		}
	})
	wg.Wait()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	ids := slices.Sorted(func(yield func(int) bool) {
		for id := range closedIDs {
			if !yield(id) {
				return
			}
		}
	})
	if next < 20 {
		t.Fatalf("only %d values built: the test did not exercise invalidation", next)
	}
	if len(ids) != next {
		t.Fatalf("%d values built, %d cleaned up", next, len(ids))
	}
	for id, n := range closedIDs {
		if n != 1 {
			t.Fatalf("value %d cleaned up %d times", id, n)
		}
	}
}
