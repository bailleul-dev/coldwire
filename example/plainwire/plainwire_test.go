package plainwire

import (
	"net/http/httptest"
	"testing"

	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/demodb"
	"github.com/bailleul-dev/coldwire/example/internal/config"
)

func TestSameBehaviorAsWire(t *testing.T) {
	cfg := config.Config{DSN: "demo://users", UserStore: "sql"}
	app := New(cfg, false)
	before := demodb.Opened.Load()
	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code, w.Body.String()
	}
	if code, _ := get("/health"); code != 200 || demodb.Opened.Load() != before {
		t.Fatal("/health must not open the pool")
	}
	for range 3 {
		if code, body := get("/users/1"); code != 200 || body != `{"id":1,"name":"grace"}`+"\n" {
			t.Fatal(code, body)
		}
	}
	if n := demodb.Opened.Load() - before; n != 1 {
		t.Fatalf("pool opened %d times", n)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
}
