package main

import (
	"errors"
	"os"
)

// fakeDebugBundler returns a test-only DebugBundler override, active ONLY when
// PYRY_ALLOW_INSECURE_RELAY=1 (the daemon's existing test/insecure marker) AND a
// fake-mode env var is set. It returns (nil, false) otherwise, so the real
// debugbundle.Assemble path is left byte-identical. Belt-and-suspenders: even a
// stray PYRY_FAKE_DEBUG_BUNDLE_* in a production environment is inert without the
// insecure-relay gate — a deterministic env compare, different fabric from any
// agent rule.
//
// The real bundler is non-deterministic (logRing.Snapshot tees every daemon log
// line, plus a timestamped recording name), so an out-of-process e2e cannot
// predict its bytes. This seam lets the e2e round-trip a known archive through
// the stream/decode path and inject a path-quoting assemble error through the
// no-leak error path — the same runtime-knob shape as the shipped PYRY_CLAUDE_BIN
// and PYRY_ALLOW_INSECURE_RELAY overrides.
//
// Happy mode (PYRY_FAKE_DEBUG_BUNDLE_FILE=<path>): the override reads the file's
// bytes on each call — those bytes are the "known archive" the e2e reassembles.
// Error mode (PYRY_FAKE_DEBUG_BUNDLE_ERR=<sentinel>): the override returns
// errors.New(sentinel), mimicking the real assembler quoting a recording path in
// its error — the sentinel the e2e asserts never reaches the wire or the logs.
// FILE takes precedence over ERR (mutually exclusive in tests).
func fakeDebugBundler() (func() ([]byte, error), bool) {
	if os.Getenv("PYRY_ALLOW_INSECURE_RELAY") != "1" {
		return nil, false
	}
	if path := os.Getenv("PYRY_FAKE_DEBUG_BUNDLE_FILE"); path != "" {
		return func() ([]byte, error) { return os.ReadFile(path) }, true
	}
	if sentinel := os.Getenv("PYRY_FAKE_DEBUG_BUNDLE_ERR"); sentinel != "" {
		return func() ([]byte, error) { return nil, errors.New(sentinel) }, true
	}
	return nil, false
}
