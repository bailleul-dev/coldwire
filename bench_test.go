package coldwire

import (
	"sync"
	"testing"
)

// Overhead of coldwire compared with the ordinary Go equivalent. The values
// are trivial on purpose, so only the mechanism is measured.

var sinkInt int

func BenchmarkGet_Eager(b *testing.B) {
	g := new(Graph)
	p := g.AsStatic(42)
	for b.Loop() {
		sinkInt = p.Get()
	}
}

func BenchmarkGet_PlainVariable(b *testing.B) {
	v := 42
	for b.Loop() {
		sinkInt = v
	}
}

func BenchmarkGet_LazyResolved(b *testing.B) {
	g := new(Graph)
	p := g.Lazily(func(Deferred) int { return 42 })
	p.Get()
	for b.Loop() {
		sinkInt = p.Get()
	}
}

func BenchmarkGet_PlainOnceValuesResolved(b *testing.B) {
	f := sync.OnceValues(func() (int, error) { return 42, nil })
	f()
	for b.Loop() {
		sinkInt, _ = f()
	}
}

func BenchmarkGet_LazyResolvedParallel(b *testing.B) {
	g := new(Graph)
	p := g.Lazily(func(Deferred) int { return 42 })
	p.Get()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = p.Get()
		}
	})
}

func BenchmarkGet_PlainOnceValuesResolvedParallel(b *testing.B) {
	f := sync.OnceValues(func() (int, error) { return 42, nil })
	f()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = f()
		}
	})
}

// Build and resolve a 3-level chain: config -> repo -> handler.
func BenchmarkChain_Coldwire(b *testing.B) {
	g := new(Graph)
	for b.Loop() {
		cfg := g.AsStatic(1)
		repo := g.Lazily(func(s Deferred) int { return cfg.From(s) + 1 })
		h := g.Lazily(func(s Deferred) int { return repo.From(s) + cfg.From(s) })
		sinkInt = h.Get()
	}
}

func BenchmarkChain_PlainOnceValues(b *testing.B) {
	for b.Loop() {
		cfg := 1
		repo := sync.OnceValues(func() (int, error) { return cfg + 1, nil })
		h := sync.OnceValues(func() (int, error) {
			r, err := repo()
			return r + cfg, err
		})
		sinkInt, _ = h()
	}
}

func BenchmarkEagerly(b *testing.B) {
	g := new(Graph)
	cfg := g.AsStatic(1)
	for b.Loop() {
		sinkInt = g.Eagerly(func(s Eager) int { return cfg.From(s) + 1 }).Get()
	}
}

// A request's lease: Acquire and release on a resolved provider.
func BenchmarkAcquireRelease(b *testing.B) {
	g := new(Graph)
	p := g.Lazily(func(Deferred) int { return 42 })
	p.Get()
	ctx := b.Context()
	for b.Loop() {
		v, lease, _ := p.Acquire(ctx)
		sinkInt = v
		lease.Release()
	}
}

func BenchmarkAcquireReleaseParallel(b *testing.B) {
	g := new(Graph)
	p := g.Lazily(func(Deferred) int { return 42 })
	p.Get()
	ctx := b.Context()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, lease, _ := p.Acquire(ctx)
			lease.Release()
		}
	})
}
