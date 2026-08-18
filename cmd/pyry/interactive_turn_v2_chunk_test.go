package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxV2AppEnvelope is the Mobile Protocol v2 application-envelope size cap
// (docs/protocol-mobile.md § Application-envelope size cap): Noise's 65535-byte
// transport message minus the 16-byte AEAD tag. Test-local on purpose, mirroring
// the same-named constant in internal/protocol's own cap test — nothing in
// cmd/pyry enforces the cap, so an exported constant here would imply an
// enforcement that lives elsewhere (in the transport).
const maxV2AppEnvelope = 65519

// TestSplitDeltaText covers the splitter in isolation: the stride boundaries,
// the rune back-up, and the byte-for-byte reassembly every caller depends on.
//
// The multi-byte rows deliberately pick a max that is NOT a multiple of the
// rune width, so a stride boundary lands mid-rune and the back-up is actually
// exercised; wantMidRuneStride pins that, so a future max cannot quietly turn
// those rows into ASCII-equivalents while they stay green.
func TestSplitDeltaText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		in                string
		max               int
		wantSizes         []int
		wantMidRuneStride bool
	}{
		{name: "empty", in: "", max: 10},
		{name: "shorter than max", in: "hello", max: 10, wantSizes: []int{5}},
		{name: "exactly max", in: strings.Repeat("a", 10), max: 10, wantSizes: []int{10}},
		{name: "one over max", in: strings.Repeat("a", 11), max: 10, wantSizes: []int{10, 1}},
		{name: "two and a half max", in: strings.Repeat("a", 25), max: 10, wantSizes: []int{10, 10, 5}},
		{
			// 3-byte runes against a max of 10: every stride boundary lands
			// one byte into a rune, so each cut backs up to 9.
			name:              "three byte runes",
			in:                strings.Repeat("世", 8),
			max:               10,
			wantSizes:         []int{9, 9, 6},
			wantMidRuneStride: true,
		},
		{
			// One ASCII byte ahead of 4-byte runes, so the rune starts sit at
			// 1, 5, 9, ... and no stride boundary is aligned either.
			name:              "offset four byte runes",
			in:                "a" + strings.Repeat("🙂", 6),
			max:               10,
			wantSizes:         []int{9, 8, 8},
			wantMidRuneStride: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantMidRuneStride && utf8.RuneStart(tc.in[tc.max]) {
				t.Fatalf("byte %d of the input is a rune start; this row no longer exercises the back-up", tc.max)
			}

			got := splitDeltaText(tc.in, tc.max)

			sizes := make([]int, len(got))
			for i, c := range got {
				sizes[i] = len(c)
			}
			if len(got) != len(tc.wantSizes) {
				t.Fatalf("chunk sizes: got %v, want %v", sizes, tc.wantSizes)
			}
			for i, want := range tc.wantSizes {
				if sizes[i] != want {
					t.Fatalf("chunk sizes: got %v, want %v", sizes, tc.wantSizes)
				}
			}
			for i, c := range got {
				if len(c) > tc.max {
					t.Errorf("chunk %d is %d B; want at most %d B", i, len(c), tc.max)
				}
				if c == "" {
					t.Errorf("chunk %d is empty; a non-empty input must not yield an empty chunk", i)
				}
				if !utf8.ValidString(c) {
					t.Errorf("chunk %d is not valid UTF-8; the split cut through a rune", i)
				}
			}
			// Byte equality, not length equality: this is the property that
			// makes the concatenation on the phone reproduce the flush exactly.
			if joined := strings.Join(got, ""); joined != tc.in {
				t.Errorf("rejoined %d B; want the %d B input back byte-for-byte", len(joined), len(tc.in))
			}
		})
	}
}

// TestSplitDeltaText_InvalidUTF8Terminates pins the degenerate guard: on input
// whose bytes are all continuation bytes, backing up to a rune start would walk
// the cut point all the way to the chunk start, emit an empty chunk and loop
// forever. The guard cuts at the unadjusted stride instead, which moves WHERE
// the split lands but never changes WHAT is emitted.
//
// This input cannot arise today — Text is decoded by encoding/json into a Go
// string, which substitutes U+FFFD for invalid bytes — so this is defence in
// depth against a future producer, not the primary control.
func TestSplitDeltaText_InvalidUTF8Terminates(t *testing.T) {
	t.Parallel()

	in := strings.Repeat("\x80", 25) // bare continuation bytes: no rune start anywhere
	got := splitDeltaText(in, 10)

	if len(got) != 3 {
		t.Fatalf("chunks: got %d, want 3", len(got))
	}
	for i, c := range got {
		if c == "" {
			t.Fatalf("chunk %d is empty; the degenerate guard did not fire", i)
		}
		if len(c) > 10 {
			t.Errorf("chunk %d is %d B; want at most 10 B", i, len(c))
		}
	}
	if joined := strings.Join(got, ""); joined != in {
		t.Errorf("rejoined %d B; want the %d B input back byte-for-byte", len(joined), len(in))
	}
}

// TestInteractiveTurnEmitterV2_OversizedDeltaFitsEnvelopeCap is the deterministic
// per-frame cap test that ENFORCES maxDeltaTextBytes — the suspenders to the
// constant's belt, and different fabric from it. It feeds ~200 KB of assistant
// text as several same-MessageID TextChunks (so the coalescer concatenates them
// into one flush), ends the turn, and measures the marshalled protocol.Envelope
// that forwardEnvelope would seal — not just its payload.
//
// The '<' row is the one that binds the constant: encoding/json HTML-escapes
// '<', '>' and '&' to a six-byte \uXXXX form, so one input byte costs six on the
// wire. An ASCII-only test would under-report by over 5x and prove nothing.
// The multi-byte rows exist for the other property — a cut through a rune is
// silently repaired by encoding/json into U+FFFD, so only byte equality on the
// rejoined text sees it.
func TestInteractiveTurnEmitterV2_OversizedDeltaFitsEnvelopeCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pieces       []string
		midRuneCheck bool // assert the naive cut would have split a rune
	}{
		{
			name:   "ascii",
			pieces: []string{strings.Repeat("a", 50000), strings.Repeat("a", 50000), strings.Repeat("a", 50000), strings.Repeat("a", 50000)},
		},
		{
			name:   "html",
			pieces: []string{strings.Repeat("<", 50000), strings.Repeat("<", 50000), strings.Repeat("<", 50000), strings.Repeat("<", 50000)},
		},
		{
			name:         "cjk",
			pieces:       []string{strings.Repeat("世", 16667), strings.Repeat("世", 16667), strings.Repeat("世", 16667), strings.Repeat("世", 16667)},
			midRuneCheck: true,
		},
		{
			name:         "mixed",
			pieces:       []string{"a" + strings.Repeat("🙂", 12500), strings.Repeat("🙂", 12500), strings.Repeat("🙂", 12500), strings.Repeat("🙂", 12500)},
			midRuneCheck: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			text := strings.Join(tc.pieces, "")
			if tc.midRuneCheck && utf8.RuneStart(text[maxDeltaTextBytes]) {
				t.Fatalf("byte %d of the fill is a rune start; this row no longer exercises the rune-safe split", maxDeltaTextBytes)
			}

			cur := &stubCursor{}
			cur.set(testConvID)
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

			for _, p := range tc.pieces {
				e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: p})
			}
			e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

			deltas := assistantDeltas(t, bcast.pushes)
			if len(deltas) < 2 {
				t.Fatalf("emitted %d assistant_delta envelopes for %d B of text; want more than one (a single frame makes every assertion below vacuous)", len(deltas), len(text))
			}

			// AC#1: the cap is measured on the whole envelope, because that is
			// what forwardEnvelope marshals and seals.
			largest := 0
			for i, p := range bcast.pushes {
				if p.env.Type != protocol.TypeAssistantDelta {
					continue
				}
				out, err := json.Marshal(p.env)
				if err != nil {
					t.Fatalf("marshal envelope %d: %v", i, err)
				}
				if len(out) > largest {
					largest = len(out)
				}
				if len(out) >= maxV2AppEnvelope {
					t.Errorf("envelope %d: got %d B, want < %d B", i, len(out), maxV2AppEnvelope)
				}
			}
			t.Logf("%s: %d B of text in %d envelopes; largest %d B, %.1f%% of the %d-byte cap",
				tc.name, len(text), len(deltas), largest,
				float64(largest)/float64(maxV2AppEnvelope)*100, maxV2AppEnvelope)

			// AC#3: byte equality, not a length or prefix check. A split
			// through a multi-byte rune is invisible to anything weaker —
			// encoding/json replaces the invalid halves with U+FFFD.
			var sb strings.Builder
			for _, d := range deltas {
				sb.WriteString(d.Text)
			}
			if sb.String() != text {
				t.Errorf("rejoined delta text is %d B; want the %d B flush back byte-for-byte", sb.Len(), len(text))
			}
			// The same property, stated more legibly: no replacement character
			// appeared that the input did not carry.
			if wantN, gotN := strings.Count(text, string(utf8.RuneError)), strings.Count(sb.String(), string(utf8.RuneError)); gotN != wantN {
				t.Errorf("rejoined text carries %d U+FFFD; input carried %d — a cut landed mid-rune", gotN, wantN)
			}

			// AC#4: one turn, and seq advancing once per emitted envelope in
			// emission order (assistantDeltas preserves Push call order).
			for i, d := range deltas {
				if d.TurnID != deltas[0].TurnID {
					t.Errorf("delta %d turn_id %q; want %q — all chunks of one flush share the turn", i, d.TurnID, deltas[0].TurnID)
				}
				if d.Seq != i {
					t.Errorf("delta %d seq %d; want %d", i, d.Seq, i)
				}
				if d.ConversationID != testConvID {
					t.Errorf("delta %d conversation_id %q; want %q", i, d.ConversationID, testConvID)
				}
			}
		})
	}
}

// TestInteractiveTurnEmitterV2_UnderCapDeltaEmitsExactlyOne pins the other half
// of AC#4: a flush that fits stays exactly one envelope with one seq, so #609's
// coalescing contract is unchanged. The maxDeltaTextBytes / +1 pair is what pins
// the stride comparison — one byte either side of the boundary.
func TestInteractiveTurnEmitterV2_UnderCapDeltaEmitsExactlyOne(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		wantSeqs []int
	}{
		{name: "short", text: "hello world", wantSeqs: []int{0}},
		{name: "exactly max", text: strings.Repeat("a", maxDeltaTextBytes), wantSeqs: []int{0}},
		{name: "one over max", text: strings.Repeat("a", maxDeltaTextBytes+1), wantSeqs: []int{0, 1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cur := &stubCursor{}
			cur.set(testConvID)
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

			e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: tc.text})
			e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

			deltas := assistantDeltas(t, bcast.pushes)
			if len(deltas) != len(tc.wantSeqs) {
				t.Fatalf("emitted %d assistant_delta envelopes; want %d", len(deltas), len(tc.wantSeqs))
			}
			var sb strings.Builder
			for i, d := range deltas {
				if d.Seq != tc.wantSeqs[i] {
					t.Errorf("delta %d seq %d; want %d", i, d.Seq, tc.wantSeqs[i])
				}
				sb.WriteString(d.Text)
			}
			if sb.String() != tc.text {
				t.Errorf("rejoined delta text is %d B; want the %d B flush back byte-for-byte", sb.Len(), len(tc.text))
			}
		})
	}
}

// TestInteractiveTurnEmitterV2_OversizedDeltaNoLogLeak is the chunking case of
// the package's no-application-output-in-logs rule (AC#5), modelled on
// TestInteractiveTurnEmitterV2_NoAppOutputLogLeak. Push fails on the one conn so
// the push_err DEBUG branch fires for EVERY chunk — the most log-heavy path a
// chunked flush can take.
func TestInteractiveTurnEmitterV2_OversizedDeltaNoLogLeak(t *testing.T) {
	t.Parallel()

	const marker = "SECRETCHUNKZZZ"
	// Marker first, so it sits wholly inside chunk 0 rather than straddling a
	// cut: a leak of any single chunk then carries it intact.
	text := marker + strings.Repeat("<", 60000)

	var buf bytes.Buffer // synchronous single-goroutine capture: bytes.Buffer is safe here
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}},
		pushErr:   map[string]error{"a": relay.ErrConnNotFound},
	}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: text})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	if deltas := assistantDeltas(t, bcast.pushes); len(deltas) < 2 {
		t.Fatalf("emitted %d assistant_delta envelopes; want more than one (the chunked path is what this test covers)", len(deltas))
	}
	logs := buf.String()
	if logs == "" {
		t.Fatal("expected DEBUG push-error logs; got none (test would not prove the no-leak property)")
	}
	if strings.Contains(logs, marker) {
		t.Fatalf("assistant text leaked into logs:\n%s", logs)
	}
}
