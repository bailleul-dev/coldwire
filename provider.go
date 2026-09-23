package coldwire

import (
	"context"
	"errors"
	"sync/atomic"
)

// Provider yields a T (or an error). K records, in the type, whether it is
// Eager or Deferred.
//
// K appears in a (zero-size) field so that Provider[T, Deferred] can never be
// converted to Provider[T, Eager].
type Provider[T any, K Kind] struct {
	n *node[T]
	_ [0]K
}

var (
	// ErrNotConstructed is returned for a zero-value Provider: typically an
	// Eager provider reading a field that is assigned later in the graph
	// constructor. Eager providers must be constructed after what they read.
	ErrNotConstructed = errors.New("provider used before it was constructed (zero value); construct it before the eager providers that read it")

	// ErrCycle is returned when resolving a Deferred provider would wait on
	// itself, on one goroutine or across several.
	ErrCycle = errors.New("dependency cycle")

	// ErrClosed is returned by every provider of a closed graph.
	ErrClosed = errors.New("graph closed")
)

// Try returns the provider's value or the error produced while building it.
// Eager providers never fail (a failure aborts construction). Deferred
// providers build on the first call and memoize the result; failures are not
// memoized, so a later call retries, but callers waiting on a failed attempt
// share its error instead of each retrying.
//
// Try takes no lease: if the value may be invalidated while you use it, use
// Acquire.
func (p Provider[T, K]) Try() (T, error) {
	if n := p.n; n != nil { // fast path, kept inlinable
		if b := n.val.Load(); b != nil {
			return b.v, nil
		}
	}
	return p.try(context.Background())
}

// TryContext is Try that stops waiting when ctx is done. The build itself
// goes on, for the other callers and the next request: it runs under the
// graph's context, not ctx.
func (p Provider[T, K]) TryContext(ctx context.Context) (T, error) {
	if n := p.n; n != nil {
		if b := n.val.Load(); b != nil {
			return b.v, nil
		}
	}
	return p.try(ctx)
}

func (p Provider[T, K]) try(ctx context.Context) (T, error) {
	v, _, err := p.resolve(ctx, nil, false)
	return v, err
}

// Acquire is TryContext with a lease: the value, and every value it was built
// from, stays alive until the lease is released, even if it is invalidated or
// its graph closed meanwhile. Release the lease when you stop using the
// value; the lease is nil on error, and releasing a nil lease does nothing,
// so this is safe:
//
//	v, lease, err := p.Acquire(ctx)
//	defer lease.Release()
//
// Package edge acquires per request.
func (p Provider[T, K]) Acquire(ctx context.Context) (T, *Lease, error) {
	v, g, err := p.resolve(ctx, nil, true)
	if err != nil {
		return v, nil, err
	}
	return v, &Lease{g: g}, nil
}

// Lease keeps a value alive; see Provider.Acquire.
type Lease struct {
	g        *gen
	released atomic.Bool
}

// Release ends the lease. Extra calls, and calls on a nil lease, do nothing.
// Cleanup errors it triggers are reported by Graph.Close.
func (l *Lease) Release() {
	if l != nil && l.released.CompareAndSwap(false, true) {
		l.g.graph.addLate(l.g.release())
	}
}

// Get is Try that panics on error. Use it at the edges of the program (see
// package edge); inside builders use From. The coldwirevet analyzer reports
// Get or Try on a Deferred provider in code that runs during construction.
func (p Provider[T, K]) Get() T {
	v, err := p.Try()
	if err != nil {
		panic(err)
	}
	return v
}

// From reads p inside a builder. s is the builder's scope: an Eager scope
// accepts only Eager providers, a Deferred scope accepts both. If p fails, the
// enclosing provider fails too, and the error records the dependency path.
// The value built by the builder holds p's value, and is invalidated with it.
func (p Provider[T, K]) From(s K) T {
	sc := s.state()
	sc.check()
	if p.n != nil && sc.att != nil {
		// Record the edge before resolving: an invalidation of p racing
		// with this build then discards the build instead of missing it.
		p.n.addDependent(sc.att.of)
	}
	v, g, err := p.resolve(sc.graph.context(), sc.att, true)
	if err != nil {
		panic(fromError{err})
	}
	sc.mu.Lock()
	sc.deps = append(sc.deps, g)
	sc.mu.Unlock()
	return v
}

// resolve returns p's value and, if ref, its generation with a reference
// taken for the caller.
func (p Provider[T, K]) resolve(ctx context.Context, from *attempt, ref bool) (T, *gen, error) {
	if p.n == nil {
		var zero T
		return zero, nil, ErrNotConstructed
	}
	return p.n.get(ctx, from, ref)
}

// fromError carries a dependency's failure out of a builder.
type fromError struct{ err error }
