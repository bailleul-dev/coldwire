// Package edge adapts providers at the edges of a program, where requests or
// invocations come in. Each adapter resolves its provider per call and holds
// a lease on it for the duration of the call: the value, and everything it
// was built from, stays open until the call returns, even if it is
// invalidated or its graph closed meanwhile. The first call builds the value;
// a build failure is handled per call and retried later.
package edge

import (
	"context"
	"net/http"

	"github.com/bailleul-dev/coldwire"
)

// Handler serves the handler held by p. While p cannot be built, onErr
// answers; if onErr is nil the response is a bare 503. A request whose
// context ends while the handler is being built stops waiting; the build
// goes on for the next requests.
func Handler[H http.Handler, K coldwire.Kind](p coldwire.Provider[H, K], onErr func(http.ResponseWriter, *http.Request, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, lease, err := p.Acquire(r.Context())
		defer lease.Release()
		if err != nil {
			if onErr != nil {
				onErr(w, r, err)
			} else {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			}
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Func turns a provider of a context-aware function, such as an AWS Lambda
// handler, into that function. A build failure becomes the call's error, and
// the call stops waiting for a build when ctx ends.
//
//	app := wire.New(config.Load())
//	lambda.Start(edge.Func(app.OrderHandler))
func Func[E, R any, K coldwire.Kind](p coldwire.Provider[func(context.Context, E) (R, error), K]) func(context.Context, E) (R, error) {
	return func(ctx context.Context, in E) (R, error) {
		f, lease, err := p.Acquire(ctx)
		defer lease.Release()
		if err != nil {
			var zero R
			return zero, err
		}
		return f(ctx, in)
	}
}
