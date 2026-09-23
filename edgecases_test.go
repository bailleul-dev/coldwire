package coldwire

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestErrorMessages(t *testing.T) {
	ce := &CloseError{Err: context.DeadlineExceeded, Building: []string{"a.go:1"}, Leased: []string{"b.go:2"}}
	if got := ce.Error(); got != "coldwire: close: context deadline exceeded; still building: a.go:1; still leased, cleaned up anyway: b.go:2" {
		t.Fatal(got)
	}
	if got := (&PanicError{Value: "bug"}).Error(); got != "panic: bug" {
		t.Fatal(got)
	}
	if KindEager.String() != "eager" || KindDeferred.String() != "deferred" {
		t.Fatal("BuildKind.String")
	}
}

func TestInvalidateZeroProvider(t *testing.T) {
	g := new(Graph)
	var p Provider[int, Deferred]
	if err := g.Invalidate(p); !errors.Is(err, ErrNotConstructed) {
		t.Fatal(err)
	}
}

func TestTryContextFastPath(t *testing.T) {
	g := new(Graph)
	p := g.AsStatic(3)
	if v, err := p.TryContext(t.Context()); v != 3 || err != nil {
		t.Fatal(v, err)
	}
}

// Constructing on a closed graph fails: an Eager provider panics with
// ErrClosed (after releasing what its builder created), a Deferred one
// returns ErrClosed.
func TestConstructOnClosedGraph(t *testing.T) {
	g := new(Graph)
	g.Close()
	d := g.Lazily(func(Deferred) int { return 1 })
	if _, err := d.Try(); !errors.Is(err, ErrClosed) {
		t.Fatalf("deferred: %v", err)
	}
	released := false
	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, ErrClosed) || !released {
			t.Fatalf("eager: %v, released=%v", err, released)
		}
	}()
	g.Eagerly(func(s Eager) int { s.OnClose(func() error { released = true; return nil }); return 1 })
}

// A build finishing after Close is discarded and releases what it created.
func TestBuildFinishingAfterClose(t *testing.T) {
	g := new(Graph)
	started, finish := make(chan struct{}), make(chan struct{})
	released := make(chan struct{})
	p := g.Lazily(func(s Deferred) int {
		s.OnClose(func() error { close(released); return nil })
		close(started)
		<-finish // ignores its context
		return 1
	})
	go p.TryContext(t.Context())
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := g.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	close(finish)
	within(t, time.Second, func() { <-released })
	if _, err := p.Try(); !errors.Is(err, ErrClosed) {
		t.Fatalf("after: %v", err)
	}
}

// The dependency path survives errors.As at every level.
func TestErrorChainLevels(t *testing.T) {
	g := new(Graph)
	a := g.TryLazily(func(Deferred) (int, error) { return 0, errBoom })
	b := g.Lazily(func(s Deferred) int { return a.From(s) })
	_, err := b.Try()
	var outer *Error
	if !errors.As(err, &outer) {
		t.Fatal(err)
	}
	var inner *Error
	if !errors.As(outer.Err, &inner) || inner.Err != errBoom {
		t.Fatalf("inner = %+v", inner)
	}
	if strings.Count(err.Error(), "edgecases_test.go:") != 2 {
		t.Fatal(err)
	}
}

// On error the lease is nil, and releasing it is a no-op, so
// `defer lease.Release()` right after Acquire is always safe.
func TestAcquireErrorGivesReleasableNilLease(t *testing.T) {
	var p Provider[int, Deferred]
	_, lease, err := p.Acquire(t.Context())
	defer lease.Release()
	if !errors.Is(err, ErrNotConstructed) || lease != nil {
		t.Fatal(err, lease)
	}
}
