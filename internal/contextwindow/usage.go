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

// defaultWindowTokens is the context window every current Claude model exposes
// (opus / sonnet / haiku are all 200K today), reported as WindowTokens for
// every session. When Anthropic ships a model whose window differs, read
// message.model from the latest usage-bearing entry and map it, keeping this
// as the unknown-model fallback.
const defaultWindowTokens = 200_000

// Usage is the current context-window occupancy derived from a transcript's
// latest usage-bearing assistant entry.
type Usage struct {
	// UsedTokens is input+cache_read+cache_creation+output on the latest
	// assistant entry that carried a usage block — the current context size,
	// not a running total across turns. Zero when no such entry exists yet.
	UsedTokens int

	// WindowTokens is the context-window size (defaultWindowTokens today).
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
	}
	return usage, nil
}
