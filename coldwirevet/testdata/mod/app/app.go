package app

import (
	"context"
	"sync"

	"example.test/dep"
	"github.com/bailleul-dev/coldwire"
)

// Graph built by a constructor function, the recommended style.
type App struct {
	coldwire.Graph
	Cfg  coldwire.Provider[string, coldwire.Eager]
	Repo coldwire.Provider[string, coldwire.Deferred]
	Out  coldwire.Provider[string, coldwire.Eager]
}

func New() *App { // want New:`forcesDeferred\(a.Repo\)`
	a := &App{}
	a.Cfg = a.AsStatic("cfg")
	a.Repo = a.Lazily(func(s coldwire.Deferred) string { return dep.DB.From(s) })
	a.Out = a.AsStatic(a.Repo.Get()) // want `Get forces Deferred provider a.Repo during graph construction in New`
	_ = dep.Indirect()               // want `call to Indirect forces a Deferred provider \(Open → DB\) during graph construction in New`
	_ = dep.Safe()
	a.Out = a.Eagerly(func(s coldwire.Eager) string {
		v, _ := a.Repo.Try()   // want `Try forces Deferred provider a.Repo during Eagerly builder`
		return v + a.Cfg.Get() // want `Get inside a Eagerly builder: use From`
	})
	a.Out = a.Eagerly(func(s coldwire.Eager) string {
		return helper() // want `call to helper forces a Deferred provider \(dep.DB\) during Eagerly builder`
	})
	_ = a.Lazily(func(s coldwire.Deferred) string {
		return a.Repo.Get() // want `Get inside a Lazily builder: use From`
	})
	_ = a.Eagerly(func(s coldwire.Eager) func() string {
		return func() string { return a.Repo.Get() } // ok: runs later
	})
	_ = a.Lazily(func(s coldwire.Deferred) func() string {
		cfg := a.Cfg.From(s) // ok: read before returning
		return func() string {
			return cfg + a.Repo.From(s) // want `scope s of the Lazily builder is used in a function that runs after the builder returned`
		}
	})
	_ = a.Lazily(func(s coldwire.Deferred) string {
		return func() string { return a.Repo.From(s) }() // ok: invoked on the spot
	})
	_ = a.Lazily(func(s coldwire.Deferred) string {
		var wg sync.WaitGroup
		var r string
		wg.Go(func() { r = a.Repo.From(s) }) // ok: parallel read, joined below
		wg.Wait()
		return r
	})
	_ = a.Lazily(func(s coldwire.Deferred) func() error {
		return func() error { return s.Context().Err() } // ok: the context outlives the build
	})
	_ = a.Lazily(func(s coldwire.Deferred) string {
		v, _ := a.Repo.TryContext(s.Context()) // want `TryContext inside a Lazily builder: use From`
		return v
	})
	_ = a.Lazily(func(s coldwire.Deferred) string {
		var wg sync.WaitGroup
		var r string
		wg.Go(func() { r = a.Repo.Get() }) // want `Get inside a Lazily builder: use From`
		go func() { _ = a.Repo.Get() }()   // want `Get inside a Lazily builder: use From`
		wg.Wait()
		return r
	})
	_ = a.Lazily(func(s coldwire.Deferred) string {
		return helper() // want `call to helper resolves a provider \(dep.DB\) with Get inside a Lazily builder`
	})
	var leaked coldwire.Deferred
	_ = a.Lazily(func(s coldwire.Deferred) coldwire.Deferred {
		leaked = s // want `scope s of the Lazily builder is stored`
		ch := make(chan coldwire.Deferred, 1)
		ch <- s                                 // want `scope s of the Lazily builder is stored`
		_ = []coldwire.Deferred{s}              // want `scope s of the Lazily builder is stored`
		_ = struct{ S coldwire.Deferred }{S: s} // want `scope s of the Lazily builder is stored`
		use(s)                                  // ok: passed to a helper
		return s                                // want `scope s of the Lazily builder is stored`
	})
	_ = leaked
	_ = a.TryLazily(func(s coldwire.Deferred) (string, error) {
		ctx := context.Background() // want `context.Background in a TryLazily builder: use s.Context\(\), which Close cancels`
		_ = ctx
		return "", s.Context().Err() // ok
	})
	_ = a.Lazily(func(s coldwire.Deferred) func() context.Context {
		return func() context.Context { return context.TODO() } // ok: runs later, not during the build
	})
	_ = a.Repo.From(nil) // want `From\(nil\) bypasses the scope check`
	return a
}

// Methods that do not construct providers run later: ok.
func (a *App) Handle() string { return a.Repo.Get() } // want Handle:`forcesDeferred\(a.Repo\)`

func use(coldwire.Deferred) {}

func helper() string { return dep.DB.Get() } // want helper:`forcesDeferred\(dep.DB\)`

// Package-level graphs are checked too.
var G coldwire.Graph

var (
	Repo = G.Lazily(func(s coldwire.Deferred) string { return dep.DB.From(s) })
	X    = G.AsStatic(Repo.Get())                // want `Get forces Deferred provider Repo during package initialization`
	Y    = func() string { return Repo.Get() }() // want `Get forces Deferred provider Repo during package initialization`
)

func init() { // want init:`forcesDeferred\(Open → DB\)`
	_ = dep.Open() // want `call to Open forces a Deferred provider \(DB\) during init`
}
