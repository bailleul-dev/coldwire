package dep

import "github.com/bailleul-dev/coldwire"

var G coldwire.Graph

var DB = G.Lazily(func(coldwire.Deferred) string { return "db" })

func Open() string { return DB.Get() } // want Open:`forcesDeferred\(DB\)`

func Indirect() string { return Open() } // want Indirect:`forcesDeferred\(Open → DB\)`

func Safe() string { return "safe" }
