// Package wire is the composition root: it plugs adapters into the hexagon's
// ports, and it is the only place that knows about coldwire. It decides, per
// component, what is built during construction (Eager) and what waits for
// first use (Deferred). Every other package exposes plain constructors.
//
//	            driving adapter          core                 driven adapter
//	            ───────────────          ────                 ──────────────
//	/users ───► UsersHTTP (D) ───► port.Users (D) ───► port.UserRepository (D)
//	                  ▲             service.Users            ├─ sqlrepo ─► DB (D)
//	                  │                                      └─ memrepo
//	            Logger (E) ◄── Config (E): picks the driven adapter
//	/health ──► Health (E)
package wire

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/bailleul-dev/coldwire"
	"github.com/bailleul-dev/coldwire/edge"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/demodb"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/memrepo"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/sqlrepo"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driving/httpapi"
	"github.com/bailleul-dev/coldwire/example/internal/config"
	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/port"
	"github.com/bailleul-dev/coldwire/example/internal/core/service"
)

// App is the dependency graph: one typed field per component.
type App struct {
	coldwire.Graph

	// Shared infrastructure.
	Config coldwire.Provider[config.Config, coldwire.Eager]
	Logger coldwire.Provider[*log.Logger, coldwire.Eager]

	// Driven side: adapters plugged into the core's outbound port.
	DB             coldwire.Provider[*sql.DB, coldwire.Deferred]
	UserRepository coldwire.Provider[port.UserRepository, coldwire.Deferred]

	// Core: the application service behind the inbound port.
	Users coldwire.Provider[port.Users, coldwire.Deferred]

	// Driving side: adapters calling the core's inbound port.
	UsersHTTP coldwire.Provider[http.Handler, coldwire.Deferred]
	Health    coldwire.Provider[http.Handler, coldwire.Eager]
}

// New builds the graph. Call it in main before serving: on Lambda that is
// the Init phase. Eager providers are built here, in this order; Deferred
// ones on first use, in any order.
func New(cfg config.Config) *App {
	a := &App{}

	a.Config = a.AsStatic(cfg)

	a.Logger = a.Eagerly(func(s coldwire.Eager) *log.Logger {
		return log.New(os.Stderr, "["+a.Config.From(s).Addr+"] ", log.LstdFlags)
	})

	// Opened on the first request that needs it; retried if it fails, and
	// closed by Close only if it was opened.
	a.DB = a.TryLazily(func(s coldwire.Deferred) (*sql.DB, error) {
		db, err := demodb.Open(s.Context(), a.Config.From(s).DSN)
		if err != nil {
			return nil, err
		}
		s.OnClose(db.Close)
		return db, nil
	})

	// The eager config picks the adapter; only the chosen branch is ever
	// built, so with UserStore "memory" the pool is never opened.
	a.UserRepository = a.TryLazily(func(s coldwire.Deferred) (port.UserRepository, error) {
		switch store := a.Config.From(s).UserStore; store {
		case "sql":
			return sqlrepo.NewUsers(a.DB.From(s)), nil
		case "memory":
			return memrepo.New(domain.User{ID: 1, Name: "grace"}, domain.User{ID: 2, Name: "ada"}), nil
		default:
			return nil, fmt.Errorf("unknown USER_STORE %q", store)
		}
	})

	a.Users = a.Lazily(func(s coldwire.Deferred) port.Users {
		return service.NewUsers(a.UserRepository.From(s))
	})

	a.UsersHTTP = a.Lazily(func(s coldwire.Deferred) http.Handler {
		return httpapi.Users(a.Users.From(s), a.Logger.From(s))
	})

	// Eager, so it is ready before the first request; reading DB,
	// UserRepository or Users here would not compile.
	a.Health = a.Eagerly(func(coldwire.Eager) http.Handler {
		return httpapi.Health()
	})

	return a
}

// Mux routes requests. Deferred handlers are resolved per request by
// edge.Handler, so a route only pays for its own dependencies, the first time
// it is hit; while they cannot be built the route answers 503.
func (a *App) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /health", a.Health.Get())
	users := edge.Handler(a.UsersHTTP, a.unavailable)
	mux.Handle("/users", users)
	mux.Handle("/users/", users)
	return mux
}

func (a *App) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	a.Logger.Get().Println(r.URL.Path, "unavailable:", err)
	http.Error(w, "unavailable", http.StatusServiceUnavailable)
}
