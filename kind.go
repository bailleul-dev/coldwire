package coldwire

import (
	"context"
	"sync"
	"sync/atomic"
)

// Kind constrains a Provider's kind to Eager or Deferred. Its method is
// unexported, so no other package can add a kind or forge a scope.
type Kind interface{ state() *scope }

// Eager is both the kind of providers built at construction time and the
// type of the scope handed to Eagerly builders. Such a scope can read Eager
// providers only.
type Eager interface {
	Kind
	eager()

	// OnClose registers f to clean up the value being built: when the value
	// is discarded (invalidated, or its graph closed) and nothing holds it
	// any more, or right away if the build fails. Call it as soon as the
	// builder has created the resource.
	OnClose(f func() error)

	// Context is the graph's context, cancelled by Graph.Close. Use it for
	// the I/O of the build. It deliberately does not carry the deadline of
	// the request that triggered the build: the value is shared.
	Context() context.Context
}

// Deferred is both the kind of providers built on first use and the type of
// the scope handed to Lazily builders. It embeds Eager, so such a scope can
// read providers of either kind.
type Deferred interface {
	Eager
	deferred()
}

// scope is valid only while its builder runs.
type scope struct {
	site  *site
	graph *Graph
	att   *attempt // the build this scope belongs to; nil for eager builds
	done  atomic.Bool

	mu      sync.Mutex
	closers []*closer
	deps    []*gen // values read with From, one reference each
}

func (s *scope) check() {
	if s.done.Load() {
		panic("coldwire: scope of the builder at " + s.site.String() + " used after the builder returned; read dependencies before returning")
	}
}

type eagerScope struct{ s *scope }

func (e eagerScope) state() *scope { return e.s }

func (eagerScope) eager() {}

func (e eagerScope) Context() context.Context { return e.s.graph.context() }

func (e eagerScope) OnClose(f func() error) {
	e.s.check()
	e.s.mu.Lock()
	e.s.closers = append(e.s.closers, &closer{fn: f})
	e.s.mu.Unlock()
}

type lazyScope struct{ eagerScope }

func (lazyScope) deferred() {}
