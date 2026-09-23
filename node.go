package coldwire

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type box[T any] struct {
	v T
	g *gen
}

type node[T any] struct {
	graph *Graph
	site  *site
	build func(*scope) (T, error) // nil for eager providers

	val atomic.Pointer[box[T]] // the current value; nil while none

	mu         sync.Mutex
	cur        *attempt // build in progress, if any
	epoch      uint64   // bumped by invalidate: builds of older epochs are discarded
	closed     bool
	dependents map[member]struct{}
}

// attempt is one run of a Deferred builder. Attempts waiting on each other
// form the wait-for graph that detects cycles.
type attempt struct {
	site  *site
	of    member
	epoch uint64
	done  chan struct{}
	err   error // set before done is closed
	// stale: the provider was invalidated or its graph closed during the
	// build; waiters must try again.
	stale bool

	waits map[*attempt]int // attempts this one waits on; guarded by waitMu
}

// waitMu guards the wait-for graph. It is only taken on the slow path, while
// builds are in progress; attempts of different graphs may wait on each
// other, so it is shared.
var waitMu sync.Mutex

// waitFor records that from waits on to, unless to already (transitively)
// waits on from: then waiting would deadlock, and the cycle is returned.
func waitFor(from, to *attempt) error {
	waitMu.Lock()
	defer waitMu.Unlock()
	if path := reach(to, from, map[*attempt]bool{}); path != nil {
		sites := make([]string, 0, len(path)+1)
		for _, a := range path {
			sites = append(sites, a.site.String())
		}
		sites = append(sites, to.site.String())
		return fmt.Errorf("%w: %s", ErrCycle, strings.Join(sites, " → "))
	}
	if from.waits == nil {
		from.waits = map[*attempt]int{}
	}
	from.waits[to]++
	return nil
}

func stopWaiting(from, to *attempt) {
	waitMu.Lock()
	defer waitMu.Unlock()
	if from.waits[to]--; from.waits[to] <= 0 {
		delete(from.waits, to)
	}
}

// reach returns a wait path from start to target, or nil.
func reach(start, target *attempt, seen map[*attempt]bool) []*attempt {
	if start == target {
		return []*attempt{start}
	}
	if seen[start] {
		return nil
	}
	seen[start] = true
	for next := range start.waits {
		if path := reach(next, target, seen); path != nil {
			return append([]*attempt{start}, path...)
		}
	}
	return nil
}

func (n *node[T]) get(ctx context.Context, from *attempt, ref bool) (T, *gen, error) {
	var zero T
	for {
		if b := n.val.Load(); b != nil && (!ref || b.g.acquire()) {
			return b.v, b.g, nil
		}
		n.mu.Lock()
		if n.closed {
			n.mu.Unlock()
			return zero, nil, ErrClosed
		}
		if b := n.val.Load(); b != nil {
			n.mu.Unlock()
			continue // published meanwhile (or being released: retry)
		}
		a := n.cur
		mine := a == nil
		if mine {
			a = &attempt{site: n.site, of: n, epoch: n.epoch, done: make(chan struct{})}
			n.cur = a
			g := n.graph
			g.mu.Lock()
			if g.inflight == nil {
				g.inflight = map[*attempt]struct{}{}
			}
			g.inflight[a] = struct{}{}
			g.mu.Unlock()
		}
		n.mu.Unlock()

		if from != nil {
			if err := waitFor(from, a); err != nil {
				if mine {
					n.finish(a, zero, nil, err) // never started: release the slot
				}
				return zero, nil, err
			}
		}
		var err error
		if mine && (from != nil || ctx.Done() == nil) {
			// Nothing to leave for: build on this goroutine. That covers Try
			// without a context and the dependencies of a build.
			n.run(a)
		} else {
			if mine {
				// Build on its own goroutine, so that this caller can leave
				// when ctx ends while the build goes on for the others.
				go n.run(a)
			}
			select {
			case <-a.done:
			case <-ctx.Done():
				err = ctx.Err()
			case <-n.graph.context().Done():
				err = ErrClosed
			}
		}
		if from != nil {
			stopWaiting(from, a)
		}
		switch {
		case err != nil:
			return zero, nil, err
		case a.stale:
			continue
		case a.err != nil:
			return zero, nil, a.err
		}
		// Built: loop to read (and reference) the published value, or to
		// build again if it was invalidated in the meantime.
	}
}

func (n *node[T]) run(a *attempt) {
	v, s, err := build(n.graph, n.site, a, KindDeferred, n.build)
	n.finish(a, v, s, err)
}

// finish publishes the outcome of attempt a. s is the builder's scope (nil if
// the builder never ran).
func (n *node[T]) finish(a *attempt, v T, s *scope, err error) {
	var closers []*closer
	var deps []*gen
	if s != nil {
		closers, deps = s.closers, s.deps
	}
	n.mu.Lock()
	n.cur = nil
	var g *gen
	if err == nil && !n.closed && a.epoch == n.epoch {
		var ok bool
		if g, ok = n.graph.newGen(n.site, closers, deps); ok {
			n.val.Store(&box[T]{v: v, g: g})
		}
	}
	if g == nil && err == nil {
		a.stale = true // invalidated or closed meanwhile
	}
	a.err = err
	n.mu.Unlock()

	if g == nil {
		// A failed, invalidated or late build: release what it created and
		// what it read.
		n.graph.addLate(errors.Join(runAll(closers), releaseAll(deps)))
	}
	gr := n.graph
	gr.mu.Lock()
	delete(gr.inflight, a)
	gr.checkDrained()
	gr.mu.Unlock()
	close(a.done)
}

func (n *node[T]) addDependent(m member) {
	if n.build == nil {
		return // eager providers are never invalidated
	}
	n.mu.Lock()
	if n.dependents == nil {
		n.dependents = map[member]struct{}{}
	}
	n.dependents[m] = struct{}{}
	n.mu.Unlock()
}

func (n *node[T]) invalidate() error {
	n.mu.Lock()
	n.epoch++
	b := n.val.Swap(nil)
	deps := n.dependents
	n.dependents = nil
	n.mu.Unlock()
	var errs []error
	for d := range deps {
		errs = append(errs, d.invalidate())
	}
	if b != nil {
		errs = append(errs, b.g.release())
	}
	return errors.Join(errs...)
}

func (n *node[T]) shut() error {
	n.mu.Lock()
	n.closed = true
	b := n.val.Swap(nil)
	n.mu.Unlock()
	if b != nil {
		return b.g.release()
	}
	return nil
}

// build runs f with a fresh scope, turning dependency failures and panics
// into an *Error located at the provider's declaration, and records it. It
// returns the scope, which holds the cleanups the builder registered and the
// values it read.
func build[T any](g *Graph, at *site, att *attempt, kind BuildKind, f func(*scope) (T, error)) (v T, s *scope, err error) {
	s = &scope{site: at, graph: g, att: att}
	start := time.Now()
	defer func() {
		s.done.Store(true)
		if r := recover(); r != nil {
			if fe, ok := r.(fromError); ok {
				err = fe.err
			} else {
				err = &PanicError{Value: r, Stack: debug.Stack()}
			}
		}
		if err != nil {
			var zero T
			v, err = zero, &Error{Site: at.String(), Err: err}
		}
		s.mu.Lock() // parallel From calls have returned: wait for their appends
		s.mu.Unlock()
		g.record(at, kind, start, time.Since(start), err)
	}()
	v, err = f(s)
	return
}
