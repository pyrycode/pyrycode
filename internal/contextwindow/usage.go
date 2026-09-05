// Package contextwindow reports the active claude session's context-window
// occupancy from its resolved transcript, so a consumer (the screen_snapshot
// handler, and any future push path) can surface a "context window used" gauge
// without each consumer re-parsing the JSONL.
//
// It reuses internal/agentrun/jsonl to decode the per-turn usage blocks rather
// than re-implementing a JSONL scanner. The transcript path it consumes is
// already canonicalised and confined upstream by the sessions resolver; this
// package opens the path it is given and does not re-resolve, re-confine, or
// re-canonicalise it.
package contextwindow

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

// defaultWindowTokens is the context window this package BELIEVES a session has,
// absent anything better to go on. It is a guess, not a fact: a 1M-context
// session exists and was measured live on 2026-09-04 (latest usage-bearing entry
// summing to 223075, which against this constant is 111%), so a session's real
// window is not knowable from the transcript's usage blocks alone.
//
// Read reports it as WindowTokens for every session whose used count does not
// contradict it, and reports 0 for one that does (see Read). Sourcing the real
// window — off claude's stream rather than by guessing — is #2101/#2102; this
// constant stays as the fallback for a session whose window is not yet known.
const defaultWindowTokens = 200_000

// Usage is the current context-window occupancy derived from a transcript's
// latest usage-bearing assistant entry.
type Usage struct {
	// UsedTokens is input+cache_read+cache_creation+output on the latest
	// assistant entry that carried a usage block — the current context size,
	// not a running total across turns. Zero when no such entry exists yet.
	UsedTokens int

	// WindowTokens is the believed context-window size (defaultWindowTokens
	// today), or 0 when UsedTokens disproved it — a used count above the
	// believed window is proof the belief is wrong, and 0 is this package's
	// "no trustworthy window reading" report. UsedTokens stays meaningful in
	// that case; only the denominator is withheld.
	WindowTokens int
}

// Read scans the claude transcript at path and reports current context-window
// usage.
//
// Two "nothing to report" inputs return Usage{UsedTokens: 0, WindowTokens:
// defaultWindowTokens} with a nil error: an empty path (the resolver's "no
// transcript resolved yet" signal, which opens nothing) and a scanned
// transcript with no assistant/usage entry yet (a fresh, pre-first-turn
// session). This deterministic zero is kept distinct from an I/O failure: a
// non-empty path that cannot be opened, or a genuine read failure mid-scan,
// returns a wrapped error with a zero Usage, so a consumer can tell "fresh
// session" from "couldn't read".
//
// A used count ABOVE the believed window disproves it, and Read then reports
// WindowTokens 0 rather than a window its own data contradicts — the daemon can
// see the contradiction without knowing anything about models (#2100). This is
// deliberately NOT an error: a wrong belief is a fact about the data, not a read
// failure, and routing it through the error path would collapse it back onto the
// disproved window. Equality is not a contradiction — a session exactly at its
// window is full, not evidence of a wrong belief — so it keeps its window.
//
// Last-usage-wins is the whole of the compaction behaviour. After an
// auto-compaction, claude's next turn records a smaller input_tokens (the
// shrunk context); because Read always reports the latest usage-bearing entry
// (no max, no running total), a post-compaction read returns the smaller,
// current figure for free — no dedicated marker needed.
//
// Safe for concurrent use: each call opens its own file and reader, neither of
// which is shared.
func Read(path string) (Usage, error) {
	if path == "" {
		return Usage{WindowTokens: defaultWindowTokens}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return Usage{}, fmt.Errorf("contextwindow: open transcript: %w", err)
	}
	defer f.Close()

	r := jsonl.NewReader(f, jsonl.Config{})
	var last *jsonl.UsageBlock
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Usage{}, fmt.Errorf("contextwindow: scan transcript: %w", err)
		}
		if ev.Usage != nil {
			last = ev.Usage
		}
	}

	usage := Usage{WindowTokens: defaultWindowTokens}
	if last != nil {
		usage.UsedTokens = last.InputTokens + last.CacheReadInputTokens +
			last.CacheCreationInputTokens + last.OutputTokens
		// Compared against the believed window rather than the constant, so this
		// keeps meaning "the reading disproves what we believed" once the window
		// is sourced per-session (#2101) instead of assumed.
		if usage.UsedTokens > usage.WindowTokens {
			usage.WindowTokens = 0
		}
	}
	return usage, nil
}
