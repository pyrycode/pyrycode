// Package main is a deliberately-broken pyry stand-in for the
// TestUpdate_BrokenNewBinary_E2E test. Build target:
//
//	go build -o /tmp/brokenpyry github.com/pyrycode/pyrycode/internal/brokenpyry
//
// On every invocation, it writes a recognizable token to stderr and exits
// non-zero. The token lets the e2e test localize "broken helper ran" from
// "some other binary exited early" cleanly.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "BROKEN_PYRY_TOKEN: broken pyry stand-in exiting non-zero")
	os.Exit(1)
}
