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
// Read reports it as WindowTokens only when the transcript's latest
// usage-bearing entry names a model the caller has observed no window for
// (#2107) — the real window is sourced off claude's stream, which the
// transcript never carries. This constant stays as the fallback for a session
// whose window is not yet known: before its first turn ends, after a daemon
// restart until the next one ends, and for any model the observed report has no
// entry for.
const defaultWindowTokens = 200_000

// Usage is the current context-window occupancy derived from a transcript's
// latest usage-bearing assistant entry.
type Usage struct {
	// UsedTokens is input+cache_read+cache_creation+output on the latest
	// assistant entry that carried a usage block — the current context size,
	// not a running total across turns. Zero when no such entry exists yet.
	UsedTokens int

	// WindowTokens is the context-window size resolved for the model that
	// produced UsedTokens — the window observed for that model when the caller
	// supplied one, and defaultWindowTokens otherwise — or 0 when UsedTokens
	// disproved whichever of the two was resolved. A used count above the
	// resolved window is proof it is wrong, and 0 is this package's "no
	// trustworthy window reading" report. UsedTokens stays meaningful in that
	// case; only the denominator is withheld.
	WindowTokens int

	// There is DELIBERATELY no model field here, and that absence is what closes
	// two channels rather than an oversight to be tidied up. jsonl.Event.Model is
	// claude-authored, unsanitized text carrying the two obligations its doc
	// states — it may not be rendered unsanitized, and it is not an argv token.
	// Read consumes it, compares it, and reports integers, so the text has no
	// route out of this package: it reaches no wire surface, no log and no spawn
	// argument. Adding it here "for diagnostics" is the single edit that would
	// reopen both, and it would also put claude-authored bytes on
	// session_settings and screen_snapshot, which #2107 has no reason to open.
}

// Read scans the claude transcript at path and reports current context-window
// usage, sizing the window from windows when the transcript's latest
// usage-bearing entry names a model that map has an entry for.
//
// # The join (#2107)
//
// windows maps a model id to the context window observed for it, as claude
// reported it on a `result` line — the transcript never carries a window, so
// the two halves arrive on different channels and this is where they meet. The
// key is the model on the SAME entry the used count came from, so the reported
// window belongs to the model that produced the reading rather than to the
// largest, the first or the only model the session has ever used.
//
// The match is EXACT and VERBATIM: no lowercasing, no date stripping, no alias
// expansion. Claude keys its map by whatever string each caller used, and no
// rule relating two spellings survives the observed data — the same model
// appears under a dated and an undated id at one window, while a genuinely
// different model appears at another. A model the transcript names that windows
// has no entry for is a MISS, and a miss falls back to defaultWindowTokens,
// which is the pre-#2107 reading exactly.
//
// AN EMPTY MODEL ID IS A MISS ON EITHER SIDE, and one guard closes both: an
// assistant entry with no message.model decodes to "", so this never looks up
// "" at all — which also makes a windows entry keyed "" unreachable. That entry
// is not hypothetical; internal/streamsup keeps a modelUsage entry keyed by the
// empty string when its window is positive, and it sorts first. An exact match
// without this rule would pair two independent empties into a confident wrong
// answer.
//
// A non-positive observed value is not a window and is treated as absent. The
// producer never emits one, but windows is caller-supplied on an exported
// signature and this contract is defended here rather than assumed upstream.
//
// windows is READ ONLY: Read neither mutates nor retains it, so a caller may
// build one per call and reuse or discard it freely. A nil map is legal and
// misses every lookup, which is exactly "nothing observed".
//
// # Nothing to report
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
// A used count ABOVE the resolved window disproves it, and Read then reports
// WindowTokens 0 rather than a window its own data contradicts — the daemon can
// see the contradiction without knowing anything about models (#2100). THE
// CHECK RUNS AFTER THE JOIN, against the window actually reported: resolve
// first and a 223075-used session on an observed 1M window reads 22%; check
// first and the same session reads "no window". Do not reorder them. This is
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
// which is shared, and no state is hung off the package. windows is read but
// never written, so two concurrent calls over one map do not race.
func Read(path string, windows map[string]int) (Usage, error) {
	if path == "" {
		// No transcript means no latest entry and therefore no model, so there is
		// nothing to join on however much windows carries.
		return Usage{WindowTokens: defaultWindowTokens}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return Usage{}, fmt.Errorf("contextwindow: open transcript: %w", err)
	}
	defer f.Close()

	r := jsonl.NewReader(f, jsonl.Config{})
	var last *jsonl.UsageBlock
	var lastModel string
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Usage{}, fmt.Errorf("contextwindow: scan transcript: %w", err)
		}
		if ev.Usage != nil {
			// The model is captured on the SAME branch as the usage block, so
			// "the latest usage-bearing entry" keeps exactly one definition and
			// the key can never come from a different entry than the count.
			last = ev.Usage
			lastModel = ev.Model
		}
	}

	usage := Usage{WindowTokens: defaultWindowTokens}
	if last != nil {
		usage.UsedTokens = last.InputTokens + last.CacheReadInputTokens +
			last.CacheCreationInputTokens + last.OutputTokens
		// Resolve, THEN check. The empty-id guard is the whole of the
		// "empty is a miss on either side" rule: a lookup that never happens
		// cannot match a windows entry keyed "".
		if lastModel != "" {
			if observed, ok := windows[lastModel]; ok && observed > 0 {
				usage.WindowTokens = observed
			}
		}
		// Compared against the resolved window rather than the constant, so this
		// keeps meaning "the reading disproves what we report" now that the
		// window is sourced per-model (#2107) rather than assumed.
		if usage.UsedTokens > usage.WindowTokens {
			usage.WindowTokens = 0
		}
	}
	return usage, nil
}
