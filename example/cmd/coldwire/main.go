// Command coldwire serves /health and /users, wired with coldwire. See package
// wire for the dependency graph and internal/ for the business code.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bailleul-dev/coldwire/example/internal/config"
	"github.com/bailleul-dev/coldwire/example/wire"
)

func main() {
	app := wire.New(config.Load())
	logger := app.Logger.Get()
	// Init profile: everything built before the first request, and its cost.
	for _, b := range app.Builds() {
		logger.Printf("built %s (%s) in %v", b.Site, b.Kind, b.Duration)
	}

	srv := &http.Server{Addr: app.Config.Get().Addr, Handler: app.Mux()}
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal(err)
		}
	}()
	<-stop.Done()

	// Drain requests first, then close what the graph built. Both are bounded:
	// CloseContext names the builds or leases it could not wait for.
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Println("shutdown:", err)
	}
	if err := app.CloseContext(ctx); err != nil {
		logger.Println("close:", err)
	}
}
