package coldwire

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

// TestRejectedAtCompileTime compiles snippets that must be rejected, and
// checks each is rejected for the intended reason.
func TestRejectedAtCompileTime(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not available")
	}
	const prelude = "var G coldwire.Graph\nvar D = G.Lazily(func(coldwire.Deferred) int { return 1 })\nvar E = G.AsStatic(1)\n"
	cases := []struct{ name, body, want string }{
		{"deferred_in_eager_builder",
			`var _ = G.Eagerly(func(s coldwire.Eager) int { return D.From(s) })`,
			"does not implement coldwire.Deferred"},
		{"deferred_in_eager_builder_among_many",
			`var _ = G.Eagerly(func(s coldwire.Eager) int { return E.From(s) + E.From(s) + D.From(s) })`,
			"does not implement coldwire.Deferred"},
		{"lazily_assigned_to_eager",
			`var _ coldwire.Provider[int, coldwire.Eager] = G.Lazily(func(s coldwire.Deferred) int { return E.From(s) })`,
			"cannot use"},
		{"convert_deferred_to_eager",
			`var _ = coldwire.Provider[int, coldwire.Eager](D)`,
			"cannot convert"},
		{"eager_scope_given_to_lazily",
			`var _ = G.Lazily(func(s coldwire.Eager) int { return 1 })`,
			"in call to G.Lazily"},
		{"forged_scope",
			"type fake struct{}\nvar _ = D.From(fake{})",
			"does not implement coldwire.Deferred"},
		{"foreign_kind",
			"type X struct{}\nvar _ coldwire.Provider[int, X]",
			"does not satisfy coldwire.Kind"},
		{"package_level_cycle",
			"var A = G.Lazily(func(s coldwire.Deferred) int { return B.From(s) })\nvar B = G.Lazily(func(s coldwire.Deferred) int { return A.From(s) })",
			"initialization cycle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, err := os.MkdirTemp(".", "negtest")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			src := "package main\nimport coldwire \"github.com/bailleul-dev/coldwire\"\n" + prelude + c.body + "\nfunc main() {}\n"
			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("go", "build", "-o", os.DevNull, "./"+dir).CombinedOutput()
			if err == nil {
				t.Fatalf("expected compile error, got success:\n%s", src)
			}
			if !strings.Contains(string(out), c.want) {
				t.Fatalf("rejected for the wrong reason (want %q):\n%s", c.want, out)
			}
		})
	}
}

func TestEagerlyRunsNow_LazilyOnce(t *testing.T) {
	g := new(Graph)
	calls := 0
	e := g.AsStatic(1)
	d := g.Lazily(func(Deferred) int { return 2 })
	ee := g.Eagerly(func(s Eager) int { calls++; return e.From(s) + 1 })
	if calls != 1 || ee.Get() != 2 {
		t.Fatal("Eagerly must run at construction")
	}
	calls = 0
	l := g.Lazily(func(s Deferred) int { calls++; return e.From(s) + d.From(s) + ee.From(s) })
	if calls != 0 {
		t.Fatal("Lazily must not run at construction")
	}
	if l.Get() != 5 || l.Get() != 5 || calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestLazilyConcurrentOnce(t *testing.T) {
	g := new(Graph)
	var calls atomic.Int32
	l := g.Lazily(func(Deferred) int { calls.Add(1); time.Sleep(time.Millisecond); return 7 })
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { l.Get() })
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

// Callers queued behind a failing attempt share its error instead of each
// hitting the failing dependency again.
func TestFailingAttemptIsShared(t *testing.T) {
	g := new(Graph)
	var calls atomic.Int32
	release := make(chan struct{})
	p := g.TryLazily(func(Deferred) (int, error) {
		calls.Add(1)
		<-release
		return 0, errBoom
	})
	var wg sync.WaitGroup
	var failed atomic.Int32
	go func() { time.Sleep(20 * time.Millisecond); close(release) }()
	for range 20 {
		wg.Go(func() {
			if _, err := p.Try(); errors.Is(err, errBoom) {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 || failed.Load() != 20 {
		t.Fatalf("attempts = %d, failures seen = %d", calls.Load(), failed.Load())
	}
	// A later call retries.
	if _, err := p.Try(); !errors.Is(err, errBoom) || calls.Load() != 2 {
		t.Fatalf("retry: attempts = %d", calls.Load())
	}
}

func TestZeroProvider(t *testing.T) {
	var p Provider[int, Eager]
	if _, err := p.Try(); err == nil {
		t.Fatal("zero provider must error")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Get on zero provider must panic")
		}
	}()
	p.Get()
}

func TestTryLazilyRetriesUntilSuccess(t *testing.T) {
	g := new(Graph)
	n := 0
	p := g.TryLazily(func(Deferred) (int, error) {
		if n++; n < 3 {
			return 0, errBoom
		}
		return n, nil
	})
	for range 2 {
		if _, err := p.Try(); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
	}
	if p.Get() != 3 || p.Get() != 3 || n != 3 {
		t.Fatalf("n = %d", n)
	}
}

func TestErrorCarriesDependencyPath(t *testing.T) {
	g := new(Graph)
	db := g.TryLazily(func(Deferred) (int, error) { return 0, errBoom })
	repo := g.Lazily(func(s Deferred) int { return db.From(s) })
	handler := g.Lazily(func(s Deferred) int { return repo.From(s) })
	_, err := handler.Try()
	if !errors.Is(err, errBoom) {
		t.Fatalf("errors.Is lost the cause: %v", err)
	}
	// Three sites, outermost first, all in this file.
	re := regexp.MustCompile(`^coldwire: coldwire/provider_test\.go:\d+ → coldwire/provider_test\.go:\d+ → coldwire/provider_test\.go:\d+: boom$`)
	if !re.MatchString(err.Error()) {
		t.Fatalf("message = %q", err.Error())
	}
	var ce *Error
	if !errors.As(err, &ce) || !strings.HasPrefix(ce.Site, "coldwire/provider_test.go:") {
		t.Fatalf("site = %+v", ce)
	}
}

func TestEagerFailureAbortsConstruction(t *testing.T) {
	g := new(Graph)
	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "provider_test.go:") {
			t.Fatalf("recovered %v", err)
		}
	}()
	g.TryEagerly(func(Eager) (int, error) { return 0, errBoom })
	t.Fatal("unreachable")
}

func TestPanicBecomesErrorAndIsRetried(t *testing.T) {
	g := new(Graph)
	n := 0
	p := g.Lazily(func(Deferred) int {
		if n++; n == 1 {
			panic("bug")
		}
		return 1
	})
	var pe *PanicError
	if _, err := p.Try(); !errors.As(err, &pe) || pe.Value != "bug" || len(pe.Stack) == 0 {
		t.Fatalf("err = %v", err)
	}
	if p.Get() != 1 {
		t.Fatal("retry after panic")
	}
}

func TestScopeUsedAfterBuilderReturnsPanics(t *testing.T) {
	g := new(Graph)
	// Escaping through a helper that stores it, which coldwirevet cannot
	// see (a closure or an assignment would be reported statically).
	dep := g.AsStatic(1)
	var leaked Deferred
	stash := func(d Deferred) { leaked = d }
	g.Lazily(func(s Deferred) int { stash(s); return 0 }).Get()
	defer func() {
		if msg, _ := recover().(string); !strings.Contains(msg, "used after the builder returned") {
			t.Fatalf("recovered %q", msg)
		}
	}()
	dep.From(leaked)
}

func TestCloseOnlyWhatWasBuilt_Reversed(t *testing.T) {
	g := new(Graph)
	var closed []string
	closer := func(name string) func() error {
		return func() error { closed = append(closed, name); return nil }
	}
	g.Eagerly(func(s Eager) int { s.OnClose(closer("eager")); return 0 })
	used := g.Lazily(func(s Deferred) int { s.OnClose(closer("used")); return 0 })
	_ = g.Lazily(func(s Deferred) int { s.OnClose(closer("unused")); return 0 })
	used.Get()
	used.Get()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if want := "used,eager"; strings.Join(closed, ",") != want {
		t.Fatalf("closed %v, want %s", closed, want)
	}
}

func TestBuildsRecordsEveryBuild(t *testing.T) {
	g := new(Graph)
	before := len(g.Builds())
	d := g.TryLazily(func(Deferred) (int, error) { time.Sleep(2 * time.Millisecond); return 0, errBoom })
	g.AsStatic(1)
	d.Try()
	got := g.Builds()[before:]
	if len(got) != 2 || got[0].Kind != KindEager || got[1].Kind != KindDeferred {
		t.Fatalf("builds = %+v", got)
	}
	if got[1].Duration < 2*time.Millisecond || !errors.Is(got[1].Err, errBoom) || !strings.Contains(got[1].Site, "provider_test.go:") {
		t.Fatalf("deferred build = %+v", got[1])
	}
	// Retries update the entry instead of growing the log.
	d.Try()
	d.Try()
	if got := g.Builds()[before:]; len(got) != 2 || got[1].Attempts != 3 {
		t.Fatalf("after retries: %+v", got)
	}
}

// In a graph built by a function, closures capture the struct, so Go cannot
// see cycles: coldwire reports them instead of deadlocking.
func TestCycleInGraphIsAnError(t *testing.T) {
	type app struct {
		Graph
		A, B, C Provider[int, Deferred]
	}
	a := &app{}
	a.A = a.Lazily(func(s Deferred) int { return a.B.From(s) })
	a.B = a.Lazily(func(s Deferred) int { return a.C.From(s) })
	a.C = a.Lazily(func(s Deferred) int { return a.A.From(s) })
	done := make(chan error, 1)
	go func() { _, err := a.A.Try(); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCycle) {
			t.Fatalf("err = %v", err)
		}
		// The cycle is listed in dependency order, closing on A.
		re := regexp.MustCompile(`dependency cycle: (coldwire/provider_test\.go:\d+) → coldwire/provider_test\.go:\d+ → coldwire/provider_test\.go:\d+ → (coldwire/provider_test\.go:\d+)$`)
		m := re.FindStringSubmatch(err.Error())
		if m == nil || m[1] != m[2] {
			t.Fatalf("message = %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock")
	}
}

func TestEagerReadingLaterFieldFailsClearly(t *testing.T) {
	type app struct {
		Graph
		Config Provider[int, Eager]
		Logger Provider[int, Eager]
	}
	a := &app{}
	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, ErrNotConstructed) {
			t.Fatalf("recovered %v", err)
		}
	}()
	a.Logger = a.Eagerly(func(s Eager) int { return a.Config.From(s) }) // Config assigned below
	a.Config = a.AsStatic(1)
}

// Sites point at the user's code even through methods promoted from the
// embedded Graph, and two graphs do not share cleanups or profiles.
func TestGraphsAreIndependent(t *testing.T) {
	type app struct {
		Graph
		DB Provider[int, Deferred]
	}
	newApp := func(closed *int) *app {
		a := &app{}
		a.DB = a.Lazily(func(s Deferred) int { s.OnClose(func() error { *closed++; return nil }); return 1 })
		return a
	}
	var c1, c2 int
	a1, a2 := newApp(&c1), newApp(&c2)
	a1.DB.Get()
	if err := a1.Close(); err != nil || c1 != 1 || c2 != 0 {
		t.Fatalf("c1=%d c2=%d", c1, c2)
	}
	if b := a1.Builds(); len(b) != 1 || !strings.HasPrefix(b[0].Site, "coldwire/provider_test.go:") {
		t.Fatalf("a1 builds = %+v", b)
	}
	if b := a2.Builds(); len(b) != 0 {
		t.Fatalf("a2 builds = %+v", b)
	}
}
