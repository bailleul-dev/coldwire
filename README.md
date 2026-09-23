# coldwire

Reflection-free, compile-time-checked dependency injection for Go, with explicit eager/deferred initialization. It is built for serverless functions, where a dependency built for nothing slows every cold start.

The dependency graph is your own struct, not a container. Each component is a typed field, filled in by a constructor function:

```go
type App struct {
    coldwire.Graph // context, built values, build profile; no lookup

    Config coldwire.Provider[config.Config, coldwire.Eager]
    Logger coldwire.Provider[*log.Logger, coldwire.Eager]
    DB     coldwire.Provider[*sql.DB, coldwire.Deferred]
    Users  coldwire.Provider[http.Handler, coldwire.Deferred]
}

func New(cfg config.Config) *App {
    a := &App{}
    a.Config = a.AsStatic(cfg)
    a.Logger = a.Eagerly(func(s coldwire.Eager) *log.Logger {
        return newLogger(a.Config.From(s))
    })
    a.DB = a.TryLazily(func(s coldwire.Deferred) (*sql.DB, error) {
        db, err := openAndPing(s.Context(), a.Config.From(s).DSN)
        if err != nil {
            return nil, err
        }
        s.OnClose(db.Close)
        return db, nil
    })
    a.Users = a.Lazily(func(s coldwire.Deferred) http.Handler {
        return users.NewHandler(a.DB.From(s), a.Logger.From(s))
    })
    // a.Eagerly(func(s coldwire.Eager) *sql.DB { return a.DB.From(s) }) // does not compile
    return a
}

func main() {
    app := New(config.Load())                             // on Lambda: the Init phase
    defer app.Close()                                     // closes only what was built, once released
    http.Handle("/users", edge.Handler(app.Users, nil))   // built on the first /users, leased per request
    http.ListenAndServe(app.Config.Get().Addr, nil)
}

// In tests, each test gets its own graph, with any configuration:
//   app := New(config.Config{UserStore: "memory"}); t.Cleanup(func() { app.Close() })
```

## Install

```sh
go get github.com/bailleul-dev/coldwire                                         # the library, no dependencies
go install github.com/bailleul-dev/coldwire/coldwirevet/cmd/coldwirevet@latest  # the analyzer
```

Requires Go 1.27 or later: the builders are generic methods.

## The rule

A `Provider[T, K]` is either **`Eager`** (built when constructed, and guaranteed ready) or **`Deferred`** (built on first use, then memoized until invalidated). You choose the kind with the builder: `Eagerly` or `Lazily`. Inside a builder, dependencies are read with `p.From(s)`. The scope `s` of an `Eagerly` builder accepts only `Eager` providers, so an eager component can never force a lazy one. A `Lazily` builder accepts both. There is one builder per kind and no limit on the number of dependencies.

How it's checked: `Eager` and `Deferred` are sealed interfaces, and `Deferred` embeds `Eager`. They serve both as the kind parameter and as the scope type, and `From` has the signature `func (p Provider[T, K]) From(s K) T`. A `Deferred` scope satisfies both kinds; an `Eager` scope does not satisfy `Deferred`. `K` is stored in the struct, so a `Provider[T, Deferred]` can't be converted to a `Provider[T, Eager]`. There is no reflection and no `unsafe`.

The builders are generic methods of `Graph`, promoted to your struct through embedding.

**Why a struct and not a container.** A container stores components and hands them out by type or by name, checked at run time. Here every dependency is a named, typed field that the compiler checks: a missing or mistyped dependency does not compile. `Graph` holds only what must be per graph: its context, the values it built, and the build profile. Build one graph in `main` and one per test; there is no global state.

## Safety

| Hazard | What coldwire does |
|---|---|
| An eager component reads a lazy one | compile error |
| A dependency cycle, on one goroutine or across several | `ErrCycle` with the path (`a.go:10 → a.go:20 → a.go:10`), instead of a deadlock |
| An eager component reads a field assigned later in `New` | construction fails with `ErrNotConstructed` and the builder's location |
| `Get()` on a Deferred provider while the graph is constructed, directly or through helpers in other packages | reported by `coldwirevet` |
| A builder's scope captured by a closure that runs later | reported by `coldwirevet`; any other escape panics at run time with the builder's location |
| An Eager provider fails | construction panics: the program fails at startup, never at the first request |
| A Deferred provider fails | `Try` returns the error; nothing is memoized, so a later call retries |
| 100 requests wait on a failing build | they share that one attempt's error instead of retrying 100 times |
| A builder panics | turned into a `*PanicError` carrying the stack: a failed call, not a panic reaching the caller |
| A build fails after creating a resource | the resource's `OnClose` runs right away |
| A request times out while a dependency is being built | it stops waiting (`TryContext`, `edge`); the build goes on for the next requests |
| Shutdown | `Close` cancels the builders' context, makes every provider return `ErrClosed`, and closes what was built, dependents before their dependencies |
| A resource goes bad (rotated credentials, lost connection) | `Invalidate` rebuilds it and everything built from it, and closes the old values |
| A request still uses a value that is invalidated or shut down | the value, and what it was built from, stays open until the request's lease is released (`Acquire`, `edge`) |
| Shutdown blocked by a request or a builder ignoring its context | `CloseContext` waits until its deadline, then cleans up anyway and names the builds and leases it could not wait for |

## Ergonomics

- **Errors carry the dependency path**, outermost first, and `errors.Is`/`errors.As` still reach the cause:

  ```
  coldwire: wire/wire.go:95 → wire/wire.go:91 → wire/wire.go:80 → wire/wire.go:69: dial tcp: connection refused
  ```

- **Context.** A builder gets `s.Context()`, the graph's context, cancelled by `Close`. It deliberately does not carry the deadline of the request that triggered the build, because the value is shared. A caller bounds its own wait with `p.TryContext(ctx)`; package `edge` passes the request's context.
- **Reference-counted values.** Each built value is held by its provider while it is current, by the values built from it, and by leases (`v, lease, err := p.Acquire(ctx); defer lease.Release()`). Its `OnClose` cleanups run when the last holder lets go. Package `edge` leases per request, so nothing is ever closed under a request.
- **Invalidation.** `app.Invalidate(app.DB)` forgets the pool. Everything built from it, found through the `From` calls, is forgotten too, and the next use rebuilds it. The old values are cleaned up, dependents first, as soon as the last request using them ends.
- **Bounded shutdown.** `app.CloseContext(ctx)` waits for leases and builds in progress until `ctx` ends. Then it cleans up anyway and returns a `*CloseError` naming what was still building or leased. Go cannot stop a goroutine, so a builder that ignores `s.Context()` is reported rather than stopped.
- **Build profile.** `app.Builds()` lists each provider built so far: where it is declared, whether it was eager or deferred, when it was built, how long it took, its last error and its number of attempts. Log it once `New` returns to see what your cold start paid for.
- **Edge adapters.** Package `edge` resolves providers where requests come in. `edge.Handler(p, onErr)` serves HTTP and answers 503 while the dependency can't be built. `edge.Func(p)` wraps a Lambda-style `func(ctx, E) (R, error)`: `lambda.Start(edge.Func(app.Handler))`.
- **Business code stays plain.** Only the graph constructor imports coldwire; the rest uses ordinary constructors.
- **Deferred providers can be declared in any order** in `New`. An Eager provider must come after what it reads, since it is built on the spot.

## API

| | |
|---|---|
| `coldwire.Graph` | embed it in your graph struct; the zero value is ready |
| `g.AsStatic(v)` | Eager provider of an existing value |
| `g.Eagerly(f)`, `g.TryEagerly(f)` | Eager provider; `f(s Eager)` runs now; an error panics |
| `g.Lazily(f)`, `g.TryLazily(f)` | Deferred provider; `f(s Deferred)` runs on first use |
| `p.From(s)` | read a dependency inside a builder |
| `s.OnClose(fn)` | register cleanup for the resource the builder created |
| `s.Context()` | the graph's context, for the build's I/O |
| `p.Acquire(ctx)` → `lease.Release()` | read at the edges, keeping the value open while in use |
| `p.Get()`, `p.Try()`, `p.TryContext(ctx)` | read without a lease (`Get` panics on error) |
| `g.Invalidate(p)` | rebuild a Deferred provider and what depends on it |
| `g.Close()`, `g.CloseContext(ctx)` | cancel, close every provider, clean up what was built once released |
| `g.Builds()` | build profile |
| `Error`, `PanicError`, `CloseError`, `ErrCycle`, `ErrNotConstructed`, `ErrClosed` | failures |
| `edge.Handler`, `edge.Func` | edge adapters |

## coldwirevet

This static analyzer catches what the type system cannot:

- `Get`/`Try`/`TryContext` on a Deferred provider while a graph is constructed: in functions that return a graph (a type embedding `coldwire.Graph`), in `Eagerly` builders, and in package-level initializers and `init`. This includes calls through helper functions, across packages.
- `Get`/`Try`/`TryContext`/`Acquire` inside a builder or in a goroutine it starts, including through helper functions. Waiting outside `From` is invisible to cycle detection.
- A scope used in a closure that outlives its builder, or stored (assigned, returned, put in a value or a channel). `s.Context()` is allowed in closures, and so are parallel reads started with `wg.Go` or errgroup's `Go`, which the builder is assumed to join.
- `context.Background()`/`context.TODO()` in a builder instead of `s.Context()`.
- `From(nil)`.

The analyzer is its own module, so the library has no dependencies. Its command bundles the standard `go vet` suite, so it replaces `vet` instead of hiding it:

```sh
go install github.com/bailleul-dev/coldwire/coldwirevet/cmd/coldwirevet@latest
go vet -vettool=$(which coldwirevet) ./...
```

It does not follow calls through interfaces or function values. A scope passed to a helper that stores it is caught at run time instead.

## Compared with plain Go

`example/wire` (coldwire) and `example/plainwire` (a function, local variables and `sync.OnceValues`) wire the same hexagonal service. They share the business packages and are both about 100 lines.

| | coldwire | plain Go |
|---|---|---|
| Eager/Deferred choice | in the type, checked by the compiler | by convention |
| Deferred forced during construction | compile error or `coldwirevet` | not detected |
| Error in a dependency | propagated by `From`, with its dependency path | `if err != nil` at each step, no path |
| Init failure | retried later; concurrent waiters share one attempt | `sync.OnceValues` keeps the error until restart |
| Panic in a lazy init | an error for that call | the panic reaches the caller, again on every call: `net/http` recovers it per request; elsewhere it can kill the process |
| Dependency cycle | `ErrCycle` with its path | deadlock (`sync.Once` re-entered) |
| Request deadline during a lazy build | the request leaves; the build continues | the request waits for the build |
| Value closed under an in-flight request | never: leases | up to you |
| Shutdown | bounded, reports stragglers | up to you |
| Rebuild after a failure mode (rotated secret) | `Invalidate`, cascading | not possible with `sync.OnceValues` |
| Close only what was built | `s.OnClose` + `Close` | hand-written flag |
| What did the cold start build? | `Builds()` | unknown |
| Tests | `New(cfg)` per test | `New(cfg)` per test |
| New concepts | scopes, `From`, kinds | none |

## Measurements

Apple M1, Go 1.27.1, macOS. Reproduce with `go test -bench . -count 6` at the root and `go run ./bench/coldstart` in `example/`.

**Per-call and construction cost** (benchstat medians):

| operation | coldwire | plain Go |
|---|---:|---:|
| `Get` on a resolved Deferred provider | 2.1 ns | 2.1 ns (`sync.OnceValues`) |
| same, 8 goroutines in parallel | 0.47 ns | 0.51 ns |
| `Get` on an Eager provider | 2.0 ns | 2.2 ns (a variable) |
| lease per request: `Acquire` + `Release` | 22 ns, 1 alloc | none |
| same, 8 goroutines in parallel | 150 ns | none |
| build and resolve a 3-provider chain, once | 2.6 µs, 35 allocs | 0.09 µs, 7 allocs |

Once a provider is resolved, `Get` costs the same as `sync.OnceValues`. A lease costs 22 ns per request. Under heavy parallelism it rises to about 150 ns, because every lease on a value updates the same counter; that is still far below the cost of an HTTP request. Each provider costs about 0.9 µs to build, once per process: 100 providers add about 90 µs to a cold start. That pays for the declaration site, the build record, the scope, cycle detection, invalidation edges and reference counting.

**Cold start end to end.** Each run starts a fresh process; the three wirings are interleaved, 40 runs each. The table shows medians, with p90 in brackets. `ready` is the time from process start to the first successful `/health`. The database connection cost is a 50 ms sleep simulated by the demo driver.

| wiring | ready | first `/users` | second `/users` |
|---|---:|---:|---:|
| coldwire | 6.1 ms (6.4) | 51.5 ms (51.6) | 160 µs (200) |
| plain Go, lazy | 7.0 ms (7.4) | 51.5 ms (51.6) | 189 µs (227) |
| plain Go, eager | 59.3 ms (60.2) | 218 µs (259) | 119 µs (142) |

Without the simulated cost, all three are ready in 5.6–5.8 ms. The first `/users` then takes 226 µs with coldwire against 198 µs in plain Go: a build triggered by a request runs on its own goroutine, so that the request can leave on its deadline. The gap in `ready` between coldwire and plain lazy at 50 ms disappears without the simulated cost, so it is not caused by coldwire.

**Reading the results:**

- coldwire does not slow down a Lambda compared with plain Go. The first request that builds a dependency pays about 30 µs more.
- Deferring does not remove work; it moves it to the first request that needs it. The gain comes from requests that never need the dependency, such as `/health`.
- These are local measurements, not Lambda or Cloud Run.

## Known limits

These come from Go itself or from what static analysis can see:

- Go cannot stop a goroutine. A builder that ignores `s.Context()` keeps running after `CloseContext`; its site is reported, and whatever it creates is cleaned up when it returns.
- `Get`, `Try` and `TryContext` take no lease. A value read that way can be closed by `Invalidate` or `Close` while still in use. Use `Acquire`, or `edge`, for anything that may be invalidated.
- `coldwirevet` does not follow calls through interfaces or function values. A `Get` hidden behind one, inside a builder, can still wait where cycle detection cannot see it.
- A scope passed to a helper function that stores it is caught only at run time, when it is used.

## Example

`example/` is a runnable hexagonal service: a core with driving and driven ports, an HTTP driving adapter, and SQL and in-memory driven adapters. `example/wire` is the graph (`wire.New`), and it is the only package that imports coldwire. `example/plainwire` is the plain-Go equivalent, and `example/cmd/{coldwire,plain}` are the two servers. Run `cd example && go test ./...`.

## Development

The repository holds three modules, tied together by `go.work` for development:

| module | contents | dependencies |
|---|---|---|
| `github.com/bailleul-dev/coldwire` | the library and `edge` | none |
| `github.com/bailleul-dev/coldwire/example` | the example service and the cold-start benchmark | the library, through `replace ../` |
| `github.com/bailleul-dev/coldwire/coldwirevet` | the analyzer and its command | `golang.org/x/tools` |

Users of the library download only the first: the example and the analyzer are not in its module archive, and their dependencies never reach your `go.mod`.

```sh
go test -race ./... ./example/... ./coldwirevet/...   # from the root, through go.work
go test -bench . -count 6                             # library benchmarks
(cd example && go run ./bench/coldstart)              # cold-start benchmark
```

The analyzer's fixtures live in `coldwirevet/testdata/mod`: a small module that uses this repository's `coldwire` through a `replace` directive, so the fixtures always compile against the real library. CI (`.github/workflows/ci.yml`) tests each module on its own with `GOWORK=off`, so that the workspace cannot hide a dependency missing from a `go.mod`. It runs on Linux, macOS and Windows, and runs `coldwirevet` on the library and the example.

## License

MIT, see [LICENSE](LICENSE).
