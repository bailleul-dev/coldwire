// Package plainwire wires the same graph as package wire in ordinary Go:
// a function, local variables and sync.OnceValues. It exists to compare the
// two approaches (see the README); the business packages are shared.
package plainwire

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/demodb"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/memrepo"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/sqlrepo"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driving/httpapi"
	"github.com/bailleul-dev/coldwire/example/internal/config"
	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/port"
	"github.com/bailleul-dev/coldwire/example/internal/core/service"
)

type App struct {
	Logger  *log.Logger
	Handler http.Handler
	Close   func() error
}

// New builds the app. If eager, the user routes (and the database) are
// built now instead of on the first /users request.
func New(cfg config.Config, eager bool) *App {
	logger := log.New(os.Stderr, "["+cfg.Addr+"] ", log.LstdFlags)

	var dbOpened atomic.Bool
	db := sync.OnceValues(func() (*sql.DB, error) {
		d, err := demodb.Open(context.Background(), cfg.DSN)
		dbOpened.Store(err == nil)
		return d, err
	})

	userRepository := sync.OnceValues(func() (port.UserRepository, error) {
		switch cfg.UserStore {
		case "sql":
			d, err := db()
			if err != nil {
				return nil, err
			}
			return sqlrepo.NewUsers(d), nil
		case "memory":
			return memrepo.New(domain.User{ID: 1, Name: "grace"}, domain.User{ID: 2, Name: "ada"}), nil
		default:
			return nil, fmt.Errorf("unknown USER_STORE %q", cfg.UserStore)
		}
	})

	usersHTTP := sync.OnceValues(func() (http.Handler, error) {
		repo, err := userRepository()
		if err != nil {
			return nil, err
		}
		return httpapi.Users(service.NewUsers(repo), logger), nil
	})
	if eager {
		if _, err := usersHTTP(); err != nil {
			logger.Fatal(err)
		}
	}

	lazy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, err := usersHTTP()
		if err != nil {
			logger.Println(r.URL.Path, "unavailable:", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.Handle("GET /health", httpapi.Health())
	mux.Handle("/users", lazy)
	mux.Handle("/users/", lazy)

	return &App{
		Logger:  logger,
		Handler: mux,
		Close: func() error {
			if !dbOpened.Load() {
				return nil
			}
			d, _ := db()
			return d.Close()
		},
	}
}
