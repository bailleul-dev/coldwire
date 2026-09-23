package edge_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bailleul-dev/coldwire"
	"github.com/bailleul-dev/coldwire/edge"
)

func TestHandlerRetriesUntilBuilt(t *testing.T) {
	g := new(coldwire.Graph)
	fail := true
	p := g.TryLazily(func(coldwire.Deferred) (http.Handler, error) {
		if fail {
			return nil, errors.New("db down")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }), nil
	})
	h := edge.Handler(p, nil)
	serve := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		return w
	}
	if w := serve(); w.Code != 503 {
		t.Fatal(w.Code)
	}
	fail = false
	if w := serve(); w.Code != 200 || w.Body.String() != "ok" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestFunc(t *testing.T) {
	g := new(coldwire.Graph)
	calls := 0
	p := g.Lazily(func(coldwire.Deferred) func(context.Context, int) (int, error) {
		calls++
		return func(_ context.Context, n int) (int, error) { return n * 2, nil }
	})
	f := edge.Func(p)
	if calls != 0 {
		t.Fatal("built too early")
	}
	for range 3 {
		if v, err := f(t.Context(), 21); v != 42 || err != nil {
			t.Fatal(v, err)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	boom := g.Lazily(func(coldwire.Deferred) func(context.Context, int) (int, error) { panic("bug") })
	var pe *coldwire.PanicError
	if _, err := edge.Func(boom)(t.Context(), 1); !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
}

// A request whose context ends while the handler is built gets onErr at its
// deadline; the build completes for the next request.
func TestHandlerRequestLeavesSlowBuild(t *testing.T) {
	g := new(coldwire.Graph)
	p := g.Lazily(func(coldwire.Deferred) http.Handler {
		time.Sleep(50 * time.Millisecond)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	})
	h := edge.Handler(p, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if w.Code != 503 || time.Since(start) > 30*time.Millisecond {
		t.Fatalf("code %d after %v", w.Code, time.Since(start))
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

// Invalidating the database during a request does not close it under that
// request: the handler it was built from is leased until the request ends.
func TestHandlerLeaseSpansTheRequest(t *testing.T) {
	type app struct {
		coldwire.Graph
		DB      coldwire.Provider[*atomic.Bool, coldwire.Deferred] // true once closed
		Handler coldwire.Provider[http.Handler, coldwire.Deferred]
	}
	a := &app{}
	inRequest, finish := make(chan struct{}), make(chan struct{})
	a.DB = a.Lazily(func(s coldwire.Deferred) *atomic.Bool {
		closed := new(atomic.Bool)
		s.OnClose(func() error { closed.Store(true); return nil })
		return closed
	})
	a.Handler = a.Lazily(func(s coldwire.Deferred) http.Handler {
		db := a.DB.From(s)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(inRequest)
			<-finish
			if db.Load() {
				w.WriteHeader(500) // used a closed database
			}
		})
	})
	h := edge.Handler(a.Handler, nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil)) }()
	<-inRequest
	if err := a.Invalidate(a.DB); err != nil {
		t.Fatal(err)
	}
	close(finish)
	<-done
	if w.Code != 200 {
		t.Fatal("the database was closed during the request")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerOnErr(t *testing.T) {
	g := new(coldwire.Graph)
	p := g.TryLazily(func(coldwire.Deferred) (http.Handler, error) { return nil, errors.New("down") })
	var got error
	h := edge.Handler(p, func(w http.ResponseWriter, _ *http.Request, err error) {
		got = err
		w.WriteHeader(502)
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 502 || got == nil || !strings.Contains(got.Error(), "down") {
		t.Fatal(w.Code, got)
	}
}
