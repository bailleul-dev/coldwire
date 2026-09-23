module github.com/bailleul-dev/coldwire/example

go 1.27.1

require github.com/bailleul-dev/coldwire v0.0.0

// The example is never a dependency: it always builds against this
// repository's coldwire.
replace github.com/bailleul-dev/coldwire => ../
