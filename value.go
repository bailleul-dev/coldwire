package coldwire

import (
	"errors"
	"sync/atomic"
)

// gen is one built value. refs counts its holders: its provider while the
// value is current, the values built from it, and leases. At zero its
// cleanups run and it releases the values it was built from.
type gen struct {
	graph   *Graph
	site    *site
	refs    atomic.Int64
	closers []*closer
	deps    []*gen // values it was built from, one reference each
}

// acquire takes a reference unless the value is already being cleaned up.
func (v *gen) acquire() bool {
	for {
		r := v.refs.Load()
		if r <= 0 {
			return false
		}
		if v.refs.CompareAndSwap(r, r+1) {
			return true
		}
	}
}

func (v *gen) release() error {
	if v.refs.Add(-1) != 0 {
		return nil
	}
	errs := []error{runAll(v.closers)}
	errs = append(errs, releaseAll(v.deps))
	g := v.graph
	g.mu.Lock()
	delete(g.live, v)
	g.checkDrained()
	g.mu.Unlock()
	return errors.Join(errs...)
}

func releaseAll(gs []*gen) error {
	var errs []error
	for i := len(gs) - 1; i >= 0; i-- {
		errs = append(errs, gs[i].release())
	}
	return errors.Join(errs...)
}

// newGen registers a successfully built value, held once by its provider.
// It reports false, without registering, if the graph is closed.
func (g *Graph) newGen(at *site, closers []*closer, deps []*gen) (*gen, bool) {
	v := &gen{graph: g, site: at, closers: closers, deps: deps}
	v.refs.Store(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, false
	}
	if g.live == nil {
		g.live = map[*gen]struct{}{}
	}
	g.live[v] = struct{}{}
	return v, true
}

// checkDrained closes g.drained once, after Close, nothing is left to wait
// for. g.mu must be held.
func (g *Graph) checkDrained() {
	if g.drained != nil && len(g.live) == 0 && len(g.inflight) == 0 {
		select {
		case <-g.drained:
		default:
			close(g.drained)
		}
	}
}
