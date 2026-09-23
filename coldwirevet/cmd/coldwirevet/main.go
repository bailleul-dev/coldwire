// Command coldwirevet runs the coldwirevet analyzer together with the
// standard go vet suite, so it can replace vet rather than run beside it:
//
//	go vet -vettool=$(which coldwirevet) ./...
package main

import (
	"github.com/bailleul-dev/coldwire/coldwirevet"
	"golang.org/x/tools/go/analysis/multichecker"
	"golang.org/x/tools/go/analysis/suite/vet"
)

func main() { multichecker.Main(append(vet.Suite, coldwirevet.Analyzer)...) }
