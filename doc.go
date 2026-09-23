// Package coldwire is reflection-free, compile-time-checked dependency
// injection with explicit eager/deferred initialization.
//
// A dependency graph is an ordinary struct that embeds Graph and holds one
// typed Provider field per component, filled by a constructor function:
//
//	type App struct {
//		coldwire.Graph
//		Config coldwire.Provider[Config, coldwire.Eager]
//		DB     coldwire.Provider[*sql.DB, coldwire.Deferred]
//	}
//
//	func New(cfg Config) *App {
//		a := &App{}
//		a.Config = a.AsStatic(cfg)
//		a.DB = a.TryLazily(func(s coldwire.Deferred) (*sql.DB, error) { return open(s.Context(), a.Config.From(s)) })
//		return a
//	}
//
// A Provider[T, K] is Eager (built when constructed, guaranteed ready) or
// Deferred (built on first use, memoized until invalidated). Dependencies are
// read inside builders with p.From(scope). The scope given to an Eagerly
// builder only accepts Eager providers, so reading a Deferred provider during
// eager construction does not compile.
//
// Every built value is reference-counted: the provider holds it while it is
// current, values built from it hold it, and so do leases taken with
// Acquire. Its cleanups (OnClose) run when the last reference goes, so a
// value is never closed under a request still using it.
package coldwire
