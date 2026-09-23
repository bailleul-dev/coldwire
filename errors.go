package coldwire

import (
	"fmt"
	"strings"
)

// Error is a build failure. Err is the cause, possibly another *Error for the
// dependency that failed; errors.Is and errors.As see through the chain.
type Error struct {
	Site string // where the failing provider is declared, as file:line
	Err  error
}

func (e *Error) Unwrap() error { return e.Err }

// Error renders the dependency path, outermost first:
// "coldwire: wire.go:75 → wire.go:48: connection refused".
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("coldwire: ")
	cur := e
	for {
		b.WriteString(cur.Site)
		next, ok := cur.Err.(*Error)
		if !ok {
			break
		}
		b.WriteString(" → ")
		cur = next
	}
	b.WriteString(": ")
	b.WriteString(cur.Err.Error())
	return b.String()
}

// PanicError is a panic raised by a builder, converted to an error so that a
// bug in a Deferred builder fails a call instead of reaching the caller.
type PanicError struct {
	Value any // the value passed to panic
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v", e.Value) }
