// Command coldstart measures cold start end to end for three wirings of the
// same service: coldwire, ordinary Go with lazy init, and ordinary Go with
// everything built before listening. For each run it starts a fresh process
// and records:
//
//	ready    process start → first successful GET /health
//	users#1  latency of the first GET /users (pays deferred init, if any)
//	users#2  latency of the second GET /users
//
// The database connection cost is simulated by the demo driver (-connect).
//
//	cd example && go run ./bench/coldstart -runs 30 -connect 50ms
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
)

type variant struct {
	name, pkg string
	env       []string
}

var variants = []variant{
	{"coldwire", "./cmd/coldwire", nil},
	{"plain Go, lazy", "./cmd/plain", nil},
	{"plain Go, eager", "./cmd/plain", []string{"EAGER=1"}},
}

func main() {
	runs := flag.Int("runs", 30, "processes started per variant")
	connect := flag.Duration("connect", 50*time.Millisecond, "simulated database connection cost")
	flag.Parse()

	dir, err := os.MkdirTemp("", "coldstart")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	fmt.Printf("runs=%d connect=%v; medians (p90)\n\n", *runs, *connect)
	fmt.Println("| wiring | binary | ready | users #1 | users #2 |")
	fmt.Println("|---|---:|---:|---:|---:|")
	type result struct{ ready, users1, users2 []time.Duration }
	bins := make([]string, len(variants))
	results := make([]result, len(variants))
	for i, v := range variants {
		bins[i] = filepath.Join(dir, filepath.Base(v.pkg))
		if out, err := exec.Command("go", "build", "-o", bins[i], v.pkg).CombinedOutput(); err != nil {
			log.Fatalf("build %s: %v\n%s", v.pkg, err, out)
		}
		once(bins[i], v.env, *connect) // warm-up: first exec of a new binary is slower
	}
	// Interleave variants so machine drift affects them equally.
	for range *runs {
		for i, v := range variants {
			r, u1, u2 := once(bins[i], v.env, *connect)
			res := &results[i]
			res.ready, res.users1, res.users2 = append(res.ready, r), append(res.users1, u1), append(res.users2, u2)
		}
	}
	for i, v := range variants {
		st, _ := os.Stat(bins[i])
		r := results[i]
		fmt.Printf("| %s | %.1f MB | %s | %s | %s |\n", v.name, float64(st.Size())/1e6, stat(r.ready), stat(r.users1), stat(r.users2))
	}
}

func once(bin string, env []string, connect time.Duration) (ready, users1, users2 time.Duration) {
	addr := freeAddr()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "ADDR="+addr, "DATABASE_URL=demo://users?connect="+connect.String(), "USER_STORE=sql")
	cmd.Env = append(cmd.Env, env...)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + addr
	for {
		if get(client, base+"/health") == nil {
			break
		}
		if time.Since(start) > 10*time.Second {
			log.Fatalf("%s never became ready", bin)
		}
		time.Sleep(100 * time.Microsecond)
	}
	ready = time.Since(start)
	users1 = timed(func() error { return get(client, base+"/users") })
	users2 = timed(func() error { return get(client, base+"/users") })
	return
}

func get(c *http.Client, url string) error {
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}

func timed(f func() error) time.Duration {
	t := time.Now()
	if err := f(); err != nil {
		log.Fatal(err)
	}
	return time.Since(t)
}

func freeAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func stat(ds []time.Duration) string {
	slices.Sort(ds)
	p := func(q float64) time.Duration { return ds[int(q*float64(len(ds)-1))] }
	return fmt.Sprintf("%s (%s)", round(p(0.5)), round(p(0.9)))
}

func round(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
}
