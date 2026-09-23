package coldwirevet_test

import (
	"path/filepath"
	"testing"

	"github.com/bailleul-dev/coldwire/coldwirevet"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	// testdata/mod is a module that uses this repository's coldwire through a
	// replace directive, so the fixtures always compile against the real
	// library.
	analysistest.Run(t, filepath.Join(analysistest.TestData(), "mod"), coldwirevet.Analyzer, "example.test/dep", "example.test/app")
}
