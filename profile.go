package coldwire

import (
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

// Builds returns one entry per provider of g built so far, in order of first
// build: what was built during construction, what was built on first use,
// how long it took, and what is failing.
func (g *Graph) Builds() []Build {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Build, len(g.built))
	for i, s := range g.built {
		out[i] = *s.rec
		out[i].Site = s.String()
	}
	return out
}

// record keeps one entry per provider, so a dependency that keeps failing
// does not grow the profile.
func (g *Graph) record(at *site, kind BuildKind, start time.Time, d time.Duration, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if at.rec == nil {
		at.rec = &Build{Kind: kind} // Site is formatted lazily, in Builds
		g.built = append(g.built, at)
	}
	at.rec.Start, at.rec.Duration, at.rec.Err = start, d, err
	at.rec.Attempts++
}

// site is a provider's declaration, captured cheaply as a program counter and
// formatted only when needed. Each constructed provider has its own site, so
// a site pointer also identifies the provider.
type site struct {
	pc  uintptr
	str atomic.Pointer[string]
	rec *Build // last build, guarded by the graph's mu; nil until first built
}

// here returns the site of the call to the exported constructor calling it.
func here() *site {
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])
	return &site{pc: pcs[0]}
}

func (s *site) String() string {
	if p := s.str.Load(); p != nil {
		return *p
	}
	str := "unknown"
	if s.pc != 0 {
		f, _ := runtime.CallersFrames([]uintptr{s.pc}).Next()
		str = filepath.Join(filepath.Base(filepath.Dir(f.File)), filepath.Base(f.File)) + ":" + strconv.Itoa(f.Line)
	}
	s.str.Store(&str)
	return str
}

// BuildKind tells whether a build ran eagerly or on first use.
type BuildKind uint8

const (
	KindEager BuildKind = iota
	KindDeferred
)

func (k BuildKind) String() string {
	if k == KindEager {
		return "eager"
	}
	return "deferred"
}

// Build describes the last build of one provider. Duration includes the
// builds of the dependencies it triggered.
type Build struct {
	Site     string
	Kind     BuildKind
	Start    time.Time
	Duration time.Duration
	Err      error // nil if the last attempt succeeded
	Attempts int   // builds run so far, including failed ones
}
