package coldwire

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// within fails the test if f does not return in time: a deadlock would hang.
func within(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("deadlock")
	}
}

// Two goroutines entering a cycle from opposite ends at the same moment: one
// of them must get ErrCycle, and neither may hang.
func TestCycleAcrossGoroutines(t *testing.T) {
	for range 50 {
		type app struct {
			Graph
			A, B Provider[int, Deferred]
		}
		a := &app{}
		var ready sync.WaitGroup
		ready.Add(2)
		gate := func() { ready.Done(); ready.Wait() } // both builds in progress before either reads
		a.A = a.Lazily(func(s Deferred) int { gate(); return a.B.From(s) })
		a.B = a.Lazily(func(s Deferred) int { gate(); return a.A.From(s) })
		var errA, errB error
		within(t, 2*time.Second, func() {
			var wg sync.WaitGroup
			wg.Go(func() { _, errA = a.A.Try() })
			wg.Go(func() { _, errB = a.B.Try() })
			wg.Wait()
		})
		if !errors.Is(errA, ErrCycle) && !errors.Is(errB, ErrCycle) {
			t.Fatalf("no cycle reported: %v / %v", errA, errB)
		}
	}
}

// A builder reading dependencies in parallel, one of which depends on the
// other, is not a cycle.
func TestParallelFanOutIsNotACycle(t *testing.T) {
	type app struct {
		Graph
		A, B, C Provider[int, Deferred]
	}
	a := &app{}
	a.C = a.Lazily(func(Deferred) int { time.Sleep(5 * time.Millisecond); return 1 })
	a.B = a.Lazily(func(s Deferred) int { return a.C.From(s) + 1 })
	a.A = a.Lazily(func(s Deferred) int {
		var b, c int
		var wg sync.WaitGroup
		wg.Go(func() { c = a.C.From(s) })
		wg.Go(func() { b = a.B.From(s) })
		wg.Wait()
		return b + c
	})
	within(t, 2*time.Second, func() {
		if v, err := a.A.Try(); v != 3 || err != nil {
			t.Errorf("got %d, %v", v, err)
		}
	})
}

// A caller whose context expires stops waiting; the build goes on and the
// next caller gets its result, built once.
func TestTryContextLeavesWhileBuildContinues(t *testing.T) {
	g := new(Graph)
	var builds atomic.Int32
	p := g.Lazily(func(Deferred) int { builds.Add(1); time.Sleep(50 * time.Millisecond); return 7 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := p.TryContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 30*time.Millisecond {
		t.Fatal("waited for the build instead of the deadline")
	}
	if v := p.Get(); v != 7 || builds.Load() != 1 {
		t.Fatalf("v=%d builds=%d", v, builds.Load())
	}
}

// Close cancels the context builders see; waiters get ErrClosed.
func TestCloseCancelsBuilds(t *testing.T) {
	g := new(Graph)
	started := make(chan struct{})
	p := g.TryLazily(func(s Deferred) (int, error) {
		close(started)
		<-s.Context().Done()
		return 0, s.Context().Err()
	})
	errc := make(chan error, 1)
	go func() { _, err := p.Try(); errc <- err }()
	<-started
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	within(t, time.Second, func() {
		if err := <-errc; !errors.Is(err, ErrClosed) && !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
	if _, err := p.Try(); !errors.Is(err, ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}
}

func TestEverythingFailsAfterClose(t *testing.T) {
	g := new(Graph)
	e := g.AsStatic(1)
	d := g.Lazily(func(Deferred) int { return 2 })
	d.Get()
	g.Close()
	if _, err := e.Try(); !errors.Is(err, ErrClosed) {
		t.Fatalf("eager: %v", err)
	}
	if _, err := d.Try(); !errors.Is(err, ErrClosed) {
		t.Fatalf("deferred: %v", err)
	}
}

// Invalidating a dependency invalidates what was built from it, runs the
// discarded values' cleanups (dependents first), and the next use rebuilds.
func TestInvalidateCascades(t *testing.T) {
	type app struct {
		Graph
		Config        Provider[string, Eager]
		DB, Repo, API Provider[string, Deferred]
		Health        Provider[string, Deferred] // does not use DB
	}
	var log []string
	var builds = map[string]int{}
	a := &app{}
	lazy := func(name string, deps ...*Provider[string, Deferred]) Provider[string, Deferred] {
		return a.Lazily(func(s Deferred) string {
			builds[name]++
			s.OnClose(func() error { log = append(log, "close "+name); return nil })
			out := name
			for _, d := range deps {
				out += "+" + d.From(s)
			}
			return out
		})
	}
	a.Config = a.AsStatic("cfg")
	a.DB = lazy("db")
	a.Repo = lazy("repo", &a.DB)
	a.API = lazy("api", &a.Repo)
	a.Health = lazy("health")
	if v := a.API.Get(); v != "api+repo+db" {
		t.Fatal(v)
	}
	a.Health.Get()

	if err := a.Invalidate(a.DB); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "close api,close repo,close db" {
		t.Fatalf("cleanups = %s", got)
	}
	a.API.Get()
	a.Health.Get()
	if builds["db"] != 2 || builds["repo"] != 2 || builds["api"] != 2 || builds["health"] != 1 {
		t.Fatalf("builds = %v", builds)
	}
}

// Invalidating while a build is in progress discards that build (and
// releases what it created); its waiters get a fresh build.
func TestInvalidateDuringBuild(t *testing.T) {
	g := new(Graph)
	var n atomic.Int32
	var closed atomic.Int32
	release := make(chan struct{})
	p := g.Lazily(func(s Deferred) int {
		v := n.Add(1)
		s.OnClose(func() error { closed.Add(1); return nil })
		if v == 1 {
			<-release
		}
		return int(v)
	})
	got := make(chan int, 1)
	go func() { got <- p.Get() }()
	time.Sleep(10 * time.Millisecond) // first build in progress
	g.Invalidate(p)
	close(release)
	within(t, time.Second, func() {
		if v := <-got; v != 2 {
			t.Errorf("got value of build %d, want the fresh build 2", v)
		}
	})
	if closed.Load() != 1 {
		t.Fatalf("discarded build not cleaned up (%d)", closed.Load())
	}
}

// A build that fails after creating a resource releases it.
func TestFailedBuildReleasesResources(t *testing.T) {
	g := new(Graph)
	released := false
	p := g.TryLazily(func(s Deferred) (int, error) {
		s.OnClose(func() error { released = true; return nil })
		return 0, errBoom
	})
	if _, err := p.Try(); !errors.Is(err, errBoom) || !released {
		t.Fatalf("err=%v released=%v", err, released)
	}
}

// Readers on the fast path race with invalidations and rebuilds: -race must
// stay quiet and every read must see a complete value.
func TestInvalidateRacesWithReaders(t *testing.T) {
	g := new(Graph)
	type pair struct{ a, b int }
	var gen atomic.Int32
	p := g.Lazily(func(Deferred) pair { v := int(gen.Add(1)); return pair{v, v} })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for ctx.Err() == nil {
				if v := p.Get(); v.a != v.b {
					t.Errorf("torn read %+v", v)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for ctx.Err() == nil {
			g.Invalidate(p)
		}
	})
	wg.Wait()
}
