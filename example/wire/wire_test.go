package wire

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/demodb"
	"github.com/bailleul-dev/coldwire/example/internal/config"
)

// newApp builds a fresh graph per test; cleanup closes what it opened.
func newApp(t *testing.T, cfg config.Config) *App {
	a := New(cfg)
	a.Logger.Get().SetOutput(io.Discard)
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func get(h http.Handler, path string) (int, string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Code, w.Body.String()
}

// End to end through the real graph: /health never opens the pool, and the
// user routes share a single pool opened on first use.
func TestColdStartAndLazyPool(t *testing.T) {
	a := newApp(t, config.Config{DSN: "demo://users", UserStore: "sql"})
	mux := a.Mux()
	before := demodb.Opened.Load()
	if code, body := get(mux, "/health"); code != 200 || body != "ok\n" {
		t.Fatal(code, body)
	}
	if n := demodb.Opened.Load() - before; n != 0 {
		t.Fatalf("/health opened the pool (%d)", n)
	}
	want := map[string]string{
		"/users":   `[{"id":2,"name":"ada"},{"id":1,"name":"grace"}]` + "\n",
		"/users/1": `{"id":1,"name":"grace"}` + "\n",
	}
	for range 3 {
		for path, body := range want {
			if code, got := get(mux, path); code != 200 || got != body {
				t.Fatalf("%s: %d %s", path, code, got)
			}
		}
	}
	if code, _ := get(mux, "/users/99"); code != 404 {
		t.Fatalf("/users/99: %d", code)
	}
	if n := demodb.Opened.Load() - before; n != 1 {
		t.Fatalf("pool opened %d times, want 1", n)
	}
}

// Another configuration is just another graph: no globals to reset.
func TestMemoryStoreNeverOpensPool(t *testing.T) {
	a := newApp(t, config.Config{UserStore: "memory"})
	before := demodb.Opened.Load()
	if code, body := get(a.Mux(), "/users"); code != 200 || body != `[{"id":2,"name":"ada"},{"id":1,"name":"grace"}]`+"\n" {
		t.Fatal(code, body)
	}
	if n := demodb.Opened.Load() - before; n != 0 {
		t.Fatalf("memory store opened the pool (%d)", n)
	}
}

// A broken dependency answers 503 with the dependency path logged, and the
// rest of the service keeps working.
func TestBrokenDatabaseIsIsolated(t *testing.T) {
	a := newApp(t, config.Config{DSN: "demo://users?connect=oops", UserStore: "sql"})
	mux := a.Mux()
	if code, _ := get(mux, "/users"); code != 503 {
		t.Fatalf("/users: %d", code)
	}
	if code, _ := get(mux, "/health"); code != 200 {
		t.Fatalf("/health: %d", code)
	}
	b := a.Builds()
	last := b[len(b)-1]
	if last.Err == nil || last.Kind.String() != "deferred" {
		t.Fatalf("profile = %+v", b)
	}
	t.Log(last.Err)
}

// After invalidating the database (credentials rotated, connection lost), the
// old pool is closed, what was built on it is rebuilt, and /health is untouched.
func TestInvalidateDatabase(t *testing.T) {
	a := newApp(t, config.Config{DSN: "demo://users", UserStore: "sql"})
	mux := a.Mux()
	before := demodb.Opened.Load()
	get(mux, "/users")
	if err := a.Invalidate(a.DB); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(mux, "/users"); code != 200 {
		t.Fatalf("/users after invalidate: %d", code)
	}
	if n := demodb.Opened.Load() - before; n != 2 {
		t.Fatalf("pool opened %d times, want 2", n)
	}
}
