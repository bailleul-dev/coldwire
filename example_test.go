package coldwire_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/bailleul-dev/coldwire"
)

type Config struct{ DSN string }

type DB struct{ dsn string }

func (db *DB) Close() error { fmt.Println("db closed"); return nil }

// App is a dependency graph: a struct embedding coldwire.Graph, with one
// typed field per component.
type App struct {
	coldwire.Graph
	Config coldwire.Provider[Config, coldwire.Eager]
	DB     coldwire.Provider[*DB, coldwire.Deferred]
	Greet  coldwire.Provider[string, coldwire.Deferred]
}

func NewApp(cfg Config) *App {
	a := &App{}
	a.Config = a.AsStatic(cfg)
	a.DB = a.TryLazily(func(s coldwire.Deferred) (*DB, error) {
		fmt.Println("opening", a.Config.From(s).DSN)
		db := &DB{dsn: a.Config.From(s).DSN}
		s.OnClose(db.Close)
		return db, nil
	})
	a.Greet = a.Lazily(func(s coldwire.Deferred) string {
		return "hello from " + a.DB.From(s).dsn
	})
	return a
}

func Example() {
	app := NewApp(Config{DSN: "postgres://prod"})
	fmt.Println("constructed; nothing opened yet")

	fmt.Println(app.Greet.Get()) // first use builds DB, then Greet
	fmt.Println(app.Greet.Get()) // memoized

	app.Close()
	// Output:
	// constructed; nothing opened yet
	// opening postgres://prod
	// hello from postgres://prod
	// hello from postgres://prod
	// db closed
}

// Invalidate rebuilds a provider and everything built from it, and cleans up
// the old values.
func ExampleGraph_Invalidate() {
	app := NewApp(Config{DSN: "postgres://prod"})
	app.Greet.Get()

	app.Invalidate(app.DB) // e.g. the credentials rotated
	fmt.Println(app.Greet.Get())

	app.Close()
	// Output:
	// opening postgres://prod
	// db closed
	// opening postgres://prod
	// hello from postgres://prod
	// db closed
}

// A lease keeps a value, and what it was built from, open while in use, even
// across an invalidation.
func ExampleProvider_Acquire() {
	app := NewApp(Config{DSN: "postgres://prod"})
	greet, lease, err := app.Greet.Acquire(context.Background())
	if err != nil {
		panic(err)
	}
	app.Invalidate(app.DB)
	fmt.Println("still usable:", greet)
	lease.Release() // the old DB closes now
	app.Close()
	// Output:
	// opening postgres://prod
	// still usable: hello from postgres://prod
	// db closed
}

// Build failures carry the dependency path and wrap the cause.
func ExampleError() {
	var g coldwire.Graph
	errDown := errors.New("connection refused")
	db := g.TryLazily(func(coldwire.Deferred) (*DB, error) { return nil, errDown })
	repo := g.Lazily(func(s coldwire.Deferred) string { return db.From(s).dsn })

	_, err := repo.Try()
	var ce *coldwire.Error
	fmt.Println(errors.Is(err, errDown), errors.As(err, &ce))
	// Output:
	// true true
}

// Builds tells what a cold start paid for.
func ExampleGraph_Builds() {
	app := NewApp(Config{DSN: "postgres://prod"})
	app.Greet.Get()
	for _, b := range app.Builds() {
		fmt.Println(b.Kind, b.Attempts, b.Err)
	}
	// Output:
	// opening postgres://prod
	// eager 1 <nil>
	// deferred 1 <nil>
	// deferred 1 <nil>
}
