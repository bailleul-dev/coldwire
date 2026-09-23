// Package demodb registers a tiny in-memory "demo" SQL driver so the example
// runs without a real database, and counts how many pools were opened.
// It understands exactly the two queries of sqlrepo: all rows, or one by id.
//
// A real driver pays network, TLS and auth costs when it opens a connection.
// This one simulates that with a sleep, set by the DSN: demo://users?connect=50ms.
package demodb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"net/url"
	"sync/atomic"
	"time"
)

// Opened counts calls to Open, so tests can prove the pool is lazy.
var Opened atomic.Int32

func init() { sql.Register("demo", drv{}) }

// Open opens a pool and pings it, so the first connection (and its simulated
// cost) is paid here, like a real adapter checking its database at startup.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	Opened.Add(1)
	db, err := sql.Open("demo", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

var table = [][]driver.Value{{int64(1), "grace"}, {int64(2), "ada"}}

type drv struct{}

func (drv) Open(dsn string) (driver.Conn, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	if d := u.Query().Get("connect"); d != "" {
		latency, err := time.ParseDuration(d)
		if err != nil {
			return nil, err
		}
		time.Sleep(latency)
	}
	return conn{}, nil
}

type conn struct{}

func (conn) Prepare(string) (driver.Stmt, error) { return stmt{}, nil }
func (conn) Close() error                        { return nil }
func (conn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

type stmt struct{}

func (stmt) Close() error                               { return nil }
func (stmt) NumInput() int                              { return -1 }
func (stmt) Exec([]driver.Value) (driver.Result, error) { return nil, driver.ErrSkip }
func (stmt) Query(args []driver.Value) (driver.Rows, error) {
	var out [][]driver.Value
	for _, row := range table {
		if len(args) == 0 || row[0] == args[0] {
			out = append(out, row)
		}
	}
	return &rows{data: out}, nil
}

type rows struct{ data [][]driver.Value }

func (*rows) Columns() []string { return []string{"id", "name"} }
func (*rows) Close() error      { return nil }
func (r *rows) Next(dest []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(dest, r.data[0])
	r.data = r.data[1:]
	return nil
}
