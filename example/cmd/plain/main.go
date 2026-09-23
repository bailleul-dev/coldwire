// Command plain serves the same routes as cmd/coldwire, wired in ordinary Go
// (package plainwire). EAGER=1 builds everything before listening.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/bailleul-dev/coldwire/example/internal/config"
	"github.com/bailleul-dev/coldwire/example/plainwire"
)

func main() {
	cfg := config.Load()
	app := plainwire.New(cfg, os.Getenv("EAGER") == "1")
	app.Logger.Println("cold start complete")
	err := http.ListenAndServe(cfg.Addr, app.Handler)
	app.Close()
	log.Fatal(err)
}
