package coldwire

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Graph owns what is per dependency graph: its context, the values it built
// and their cleanups, and the build profile. Embed it in the struct that holds
// your providers; its zero value is ready to use. It stores no components and
// offers no lookup: dependencies are your struct's typed fields.
type Graph struct {
	mu       sync.Mutex
	closed   bool
	members  []member              // every provider constructed, in order
	live     map[*gen]struct{}     // values not yet cleaned up
	inflight map[*attempt]struct{} // Deferred builds running
	late     []error               // cleanup errors from releases after Invalidate/Close
	drained  chan struct{}         // closed when, after Close, live and inflight are empty
	built    []*site               // one entry per provider built, in order of first build

	ctxOnce sync.Once
	ctx     context.Context
	cancel  context.CancelFunc
}

// context is the graph's lifetime: builders see it through Eager.Context,
// and Close cancels it.
func (g *Graph) context() context.Context {
	g.ctxOnce.Do(func() { g.ctx, g.cancel = context.WithCancel(context.Background()) })
	return g.ctx
}

// member is what the graph and invalidation need from a provider's node,
// whatever its type.
type member interface {
	invalidate() error
	shut() error
}

type closer struct {
	fn   func() error
	done atomic.Bool
}

func (c *closer) run() error {
	if c.done.CompareAndSwap(false, true) {
		return c.fn()
	}
	return nil
}

// runAll runs cs in reverse order and joins their errors.
func runAll(cs []*closer) error {
	var errs []error
	for i := len(cs) - 1; i >= 0; i-- {
		if err := cs[i].run(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// join adds a newly constructed provider to g.
func (g *Graph) join(m member) (closed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.members = append(g.members, m)
	return g.closed
}

func (g *Graph) addLate(err error) {
	if err == nil {
		return
	}
	g.mu.Lock()
	g.late = append(g.late, err)
	g.mu.Unlock()
}

// Close is CloseContext without a deadline: it waits for every lease to be
// released and every build in progress to finish.
func (g *Graph) Close() error { return g.CloseContext(context.Background()) }

// CloseContext shuts the graph down. It cancels the builders' context, makes
// every provider return ErrClosed, and drops the providers' references, most
// recently constructed first: values nobody else holds are cleaned up at
// once, dependents before their dependencies. Values still leased, or still
// used by values that are, are cleaned up when released; CloseContext waits
// for that and for the builds in progress, until ctx ends. Then it cleans up
// what remains anyway and returns a *CloseError naming the builds still
// running and the values still leased: Go cannot stop a goroutine, so a
// builder that ignores its context is reported rather than stopped.
//
// It returns the cleanup errors, including those of values released since
// the last Invalidate or Close. Calling it again returns nil.
func (g *Graph) CloseContext(ctx context.Context) error {
	g.context()
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	g.drained = make(chan struct{})
	members := slices.Clone(g.members)
	g.mu.Unlock()
	g.cancel()

	var errs []error
	for _, m := range slices.Backward(members) {
		errs = append(errs, m.shut())
	}
	g.mu.Lock()
	g.checkDrained()
	g.mu.Unlock()

	select {
	case <-g.drained:
	case <-ctx.Done():
		g.mu.Lock()
		ce := &CloseError{Err: ctx.Err()}
		var forced []*gen
		for v := range g.live {
			forced = append(forced, v)
			ce.Leased = append(ce.Leased, v.site.String())
		}
		for a := range g.inflight {
			ce.Building = append(ce.Building, a.site.String())
		}
		g.mu.Unlock()
		for _, v := range forced {
			errs = append(errs, runAll(v.closers))
		}
		slices.Sort(ce.Leased)
		slices.Sort(ce.Building)
		errs = append(errs, ce)
	}
	g.mu.Lock()
	errs = append(errs, g.late...)
	g.late = nil
	g.mu.Unlock()
	return errors.Join(errs...)
}

// CloseError reports what CloseContext could not wait for.
type CloseError struct {
	Err      error    // why it stopped waiting: ctx.Err()
	Building []string // sites of the builders still running
	Leased   []string // sites of the values still leased, cleaned up anyway
}

func (e *CloseError) Unwrap() error { return e.Err }

func (e *CloseError) Error() string {
	var b strings.Builder
	b.WriteString("coldwire: close: ")
	b.WriteString(e.Err.Error())
	if len(e.Building) > 0 {
		b.WriteString("; still building: " + strings.Join(e.Building, ", "))
	}
	if len(e.Leased) > 0 {
		b.WriteString("; still leased, cleaned up anyway: " + strings.Join(e.Leased, ", "))
	}
	return b.String()
}

// Invalidate forgets p's value so that the next use builds it again, for
// example after its credentials rotated or its connection broke. Values built
// from it (directly or not) are invalidated too. Each discarded value is
// cleaned up once nothing holds it any more: at once if unused, or when the
// last lease on it, or on a value built from it, is released. A build of p in
// progress is discarded when it completes, and its waiters retry.
//
// It returns the cleanup errors of the values cleaned up at once; errors of
// later cleanups are returned by Close.
func (g *Graph) Invalidate[T any](p Provider[T, Deferred]) error {
	if p.n == nil {
		return ErrNotConstructed
	}
	return p.n.invalidate()
}

// AsStatic wraps an already computed value.
func (g *Graph) AsStatic[T any](v T) Provider[T, Eager] {
	return eagerOf(g, here(), func(*scope) (T, error) { return v, nil })
}

// Eagerly runs f now and wraps its result. f can only read Eager providers:
// passing its scope to a Deferred provider's From does not compile.
func (g *Graph) Eagerly[T any](f func(s Eager) T) Provider[T, Eager] {
	return eagerOf(g, here(), func(s *scope) (T, error) { return f(eagerScope{s}), nil })
}

// TryEagerly is Eagerly with a fallible f. An error (or panic) aborts
// construction with a panic, so an Eager provider is always ready: the
// program fails while building the graph, with the dependency path.
func (g *Graph) TryEagerly[T any](f func(s Eager) (T, error)) Provider[T, Eager] {
	return eagerOf(g, here(), func(s *scope) (T, error) { return f(eagerScope{s}) })
}

// Lazily defers f until first use. f can read providers of either kind.
func (g *Graph) Lazily[T any](f func(s Deferred) T) Provider[T, Deferred] {
	return lazyOf(g, here(), func(s *scope) (T, error) { return f(lazyScope{eagerScope{s}}), nil })
}

// TryLazily is Lazily with a fallible f. Failures (errors, panics, failed
// dependencies, cycles) are returned by Try and retried on a later call.
func (g *Graph) TryLazily[T any](f func(s Deferred) (T, error)) Provider[T, Deferred] {
	return lazyOf(g, here(), func(s *scope) (T, error) { return f(lazyScope{eagerScope{s}}) })
}

func eagerOf[T any](g *Graph, at *site, f func(*scope) (T, error)) Provider[T, Eager] {
	v, s, err := build(g, at, nil, KindEager, f)
	var gv *gen
	if err == nil {
		var ok bool
		if gv, ok = g.newGen(at, s.closers, s.deps); !ok {
			err = &Error{Site: at.String(), Err: ErrClosed}
		}
	}
	if err != nil {
		runAll(s.closers)
		releaseAll(s.deps)
		panic(err)
	}
	n := &node[T]{graph: g, site: at}
	n.val.Store(&box[T]{v: v, g: gv})
	g.join(n)
	return Provider[T, Eager]{n: n}
}

func lazyOf[T any](g *Graph, at *site, f func(*scope) (T, error)) Provider[T, Deferred] {
	n := &node[T]{graph: g, site: at, build: f}
	n.closed = g.join(n)
	return Provider[T, Deferred]{n: n}
}
