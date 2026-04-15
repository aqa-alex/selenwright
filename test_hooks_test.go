// PR #22 (audit H-1): explicit opt-in for the init()-time error
// swallowing previously gated by testing.Testing(). Placing the flip
// in a _test.go file confines the behavior to `go test` builds; a
// coverage-instrumented production binary (`go build -cover`) does
// not include this file and therefore keeps the fatal-on-error
// semantics that operators expect.
//
// The package-level var initializer runs before main.init(), so the
// flag is visible by the time init() evaluates it.

package main

var _ = func() bool {
	testHooksEnabled = true
	return true
}()
