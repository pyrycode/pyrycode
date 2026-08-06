//go:build e2e_realclaude

package realclaude

// The result-trailer observation #1266 ships: a pure scan over pyry's stdout
// that keeps "the trailer is there", "it is not" and "the scan could not read
// the bytes" apart, plus a poll that stamps WHEN the trailer was first seen
// together with a bound on how late that stamp may be.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence — classifying a run's observations is
// #1267's work. Everything here runs offline: no live claude, no credentials,
// no daemon, no env gate, no t.Skip.
//
//	go test -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
//
// # What the bound bounds, and what it does not
//
//	pyry writes trailer ──(gap A: the os/exec copier)──> visible in the buffer
//	                    ──(gap B: the 200 ms poll tick)──> ObservedAt
//	                                     └─ Staleness bounds gap B only ─┘
//
// Gap A is unmeasured and unbounded here: measuring it needs a timestamp inside
// pyry's own emitter, which is a production change this probe-family ticket does
// not make. So Staleness says how long the trailer had been VISIBLE IN THE
// BUFFER, never how long ago pyry wrote it. A record claiming the latter would
// be the same category of lie the bound's discriminator exists to prevent.
//
// # Reused, not rebuilt
//
// resultTrailer (tool_loop_test.go:194) is the decode, reachCapCommand
// (background_reach_probe_test.go:945) the cap, probeSyncBuffer and
// probePollInterval (background_trigger_probe_test.go:722, :131) the buffer and
// the tick. parseResultTrailer and its nine call sites are NOT edited: all nine
// want today's two-valued behaviour, so widening its error contract for this
// instrument's benefit would be a nine-site cascade that gains none of them
// anything. The new scan lives alongside it and copies its matching rule
// character for character, because two classifiers of the same bytes that
// disagreed would be worse than either.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- the two value spaces ----------------------------------------------------

// What a scan over stdout found. Three states, never collapsed. Each is a
// non-empty distinct string so that no zero-valued field can read as one of
// them: a trailObservation nobody filled in is INVALID, never "the trailer
// never appeared".
const (
	// trailSeen: a type:result line was found and decoded.
	trailSeen = "trailer-seen"
	// trailAbsent: the scan read every line cleanly and none was a trailer.
	// A statement about the bytes, and only reachable when the scanner itself
	// reported no error.
	trailAbsent = "trailer-absent"
	// trailAborted: the scan could not read the bytes — today only a line past
	// bufio.Scanner's 64 KiB default. Never an answer about pyry. This is the
	// state parseResultTrailer cannot express: it discards scanner.Err(), so an
	// over-long line returns the same "no type:result line" as a genuine
	// absence, and a consumer with a named nothing-was-measured outcome would
	// file the instrument's own breakage under it.
	trailAborted = "scan-aborted"
)

// What the staleness bound was measured FROM. The third value is not a collapse
// of the first two: it is the record saying no bound exists at all.
const (
	// trailBoundFromMiss: measured from the last poll that did NOT match, so it
	// genuinely bounds how long the trailer had been visible. The only value a
	// consumer may treat as a bound.
	trailBoundFromMiss = "since-last-non-matching-poll"
	// trailBoundFromStart: the first poll already matched, so no non-matching
	// poll was ever observed and the duration is measured from the loop's start.
	// It BOUNDS NOTHING — the trailer may have become visible before the loop
	// began. A consumer tells this case apart from the one above by reading this
	// discriminator, never by reading prose.
	trailBoundFromStart = "since-poll-start"
	// trailBoundNone: no bound was measured, which is the honest report for the
	// absent and aborted states. Distinct and non-empty precisely so a record
	// carrying no bound can never read as one bounded by a non-matching poll.
	trailBoundNone = "no-bound-measured"
)

// --- the record --------------------------------------------------------------

// trailScanResult is what a pure scan over stdout bytes produces. It carries no
// instant and no bound, because a pure function has no clock — a value with a
// zero time.Time in a field named "the observation instant" is a lie the type
// system can prevent, so the two are separate types.
type trailScanResult struct {
	State string `json:"state"`
	// Line is the matched line VERBATIM, capped by reachCapCommand as it enters
	// the record. Empty unless State == trailSeen.
	//
	// It holds verbatim model output: the trailer's `result` field is the last
	// assistant message, sixth on emitter.go:456-468's pinned wire order, so
	// roughly 415 of the retained 512 bytes are text the model chose. Treat this
	// field as OPERATOR-REVIEW-BEFORE-PASTE. Trailer below is not — see there.
	Line string `json:"trailer_line,omitempty"`
	// Trailer is the decode of the FULL line, not of Line. nil unless
	// State == trailSeen, and a pointer deliberately: a consumer that
	// dereferences it without checking State panics loudly, which is strictly
	// better than a value type handing it TerminalReason == "" and letting an
	// empty terminal reason pass as a real one.
	//
	// resultTrailer has no `result` member, so this value structurally CANNOT
	// carry the assistant payload — that is a property to preserve, not an
	// omission to fix. It is what makes the cap safe to apply to Line alone:
	// truncation degrades the human-readable evidence and never a field the
	// consumer branches on.
	Trailer *resultTrailer `json:"trailer,omitempty"`
	// KeyNames is the sorted list of the FULL line's top-level JSON key NAMES,
	// read by trailKeyNames (trailer_key_names_test.go:87) before the cap and
	// carrying no value from the line. Empty unless State == trailSeen.
	//
	// It exists because the fixed decode above cannot answer one question: with
	// TerminalReason a plain omitempty string, an ABSENT terminal_reason and one
	// emitted as "" are both "". Here they are not — the names say which keys the
	// line carried, so claude's own result line (no terminal_reason at all, the
	// healthy shape on the headless path) is distinguishable from pyry's
	// synthesised idle-stall trailer without widening resultTrailer.
	//
	// Names only, and unlike Line that is structural rather than a discipline:
	// trailKeyNames returns []string and discards its map[string]json.RawMessage
	// internally, so no value can cross. This field is NOT
	// operator-review-before-paste.
	KeyNames []string `json:"trailer_keys,omitempty"`
	Detail   string   `json:"detail"`
}

// trailObservation is a complete observation: a scan result plus when it was
// taken and how late it may be.
type trailObservation struct {
	trailScanResult
	// ObservedAt is when the poll that produced this result was stamped —
	// BEFORE it read the buffer, see trailWaitForTrailer.
	ObservedAt time.Time `json:"observed_at"`
	// Staleness bounds how long the trailer had already been VISIBLE IN THE
	// BUFFER when ObservedAt was stamped (gap B in the file header). It says
	// nothing about how long ago pyry wrote it. Meaningful only when BoundFrom
	// is trailBoundFromMiss; zero otherwise. The unit is in the json key because
	// encoding/json renders a Duration as a bare nanosecond count.
	Staleness time.Duration `json:"staleness_ns"`
	BoundFrom string        `json:"staleness_from"`
}

// --- the scan ----------------------------------------------------------------

// trailScan answers "is there a result trailer in these stdout bytes?" over a
// snapshot of them.
//
// Pure over its bytes: no exec, no file read, no clock. That is what lets every
// arm be driven with no live turn and no credentials. It takes no *testing.T and
// never fails a test — an instrument failure observed mid-turn is a datum to
// publish, not a reason to abort the turn, the same contract as
// tdnClassifyReapLog, pinReadState and fifoLiveRead.
//
// The matching rule is parseResultTrailer's, unchanged: unmarshal each line into
// a resultTrailer, skip the ones that fail, take the first whose Type is
// "result". A malformed line is ordinary input — a partial trailing line
// mid-write fails to unmarshal and is simply not a match yet.
//
// scanner.Err() is consulted only AFTER the loop, so the early return at a match
// has an observable consequence worth stating: an over-long line that appears
// AFTER the trailer never affects the answer, because the scan has already
// returned. Only an over-long line at or before the trailer's position aborts.
// The answer is about the trailer, and the trailer was read whole.
//
// The buffer is deliberately NOT raised past bufio.Scanner's 64 KiB default.
// Raising it only moves the threshold; reading scanner.Err() is what separates
// "no trailer" from "unreadable".
func trailScan(stdout []byte) trailScanResult {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	lines := 0
	for scanner.Scan() {
		lines++
		var tr resultTrailer
		if err := json.Unmarshal(scanner.Bytes(), &tr); err != nil {
			continue
		}
		if tr.Type != "result" {
			continue
		}
		return trailScanResult{
			State: trailSeen,
			// The cap applies to the retained COPY only. The decode above ran
			// against the full line, which is why terminal_reason — last on the
			// wire and the field #1267 branches on — survives a truncation that
			// lands inside the `result` field sixth on the wire.
			//
			// The key-name reading takes scanner.Bytes() for the SAME reason, and
			// deliberately not Line: a capped realistic trailer is truncated JSON,
			// so a reader fed the copy below returns NO names at all while staying
			// correct on every short fixture. Its failure arm is unreachable from
			// here — this return is past tr.Type == "result", settable only from a
			// JSON object, so the map decode always succeeds and always carries at
			// least `type`.
			Line:     reachCapCommand(string(scanner.Bytes())),
			Trailer:  &tr,
			KeyNames: trailKeyNames(scanner.Bytes()),
			Detail: reachCapCommand(fmt.Sprintf("a type:result line was found at line %d of the "+
				"%d bytes scanned; the decode ran against the FULL line, so every field the "+
				"trailer carries survives the %d-byte cap applied to the recorded copy",
				lines, len(stdout), reachMaxCommandBytes)),
		}
	}

	if err := scanner.Err(); err != nil {
		return trailScanResult{
			State: trailAborted,
			Detail: reachCapCommand(fmt.Sprintf("the scan aborted after %d line(s) of %d bytes: "+
				"%v. Recorded as %s and never as %s, because a line past bufio.Scanner's 64 KiB "+
				"default would otherwise read as \"pyry never finished the turn\" — this "+
				"instrument's own breakage filed under an answer about pyry",
				lines, len(stdout), err, trailAborted, trailAbsent)),
		}
	}

	return trailScanResult{
		State: trailAbsent,
		Detail: reachCapCommand(fmt.Sprintf("no type:result line in %d bytes of stdout, across %d "+
			"line(s) read to the end with the scanner reporting no error. An absence of the "+
			"trailer, not an unreadable buffer", len(stdout), lines)),
	}
}

// --- the poll ----------------------------------------------------------------

// trailWaitForTrailer polls stdout until a result trailer is visible, the scan
// aborts, or the timeout expires. It never fails a test; every outcome is a
// record.
//
// # The stamping order is the whole correctness argument
//
// Each iteration stamps `now` BEFORE it reads the buffer. A miss poll stamped
// after its read could postdate the append that made the trailer visible, and
// the resulting now-lastMiss would then be SMALLER than the true lateness — a
// non-bound wearing a bound's label, which is exactly the defect this instrument
// exists to close. Stamped before the read,
//
//	lastMiss <= readInstant <= visibilityInstant
//
// holds unconditionally, and therefore so does
//
//	Staleness >= ObservedAt - visibilityInstant.
//
// # Returning immediately on an aborted scan is sound
//
// probeSyncBuffer.Write only ever appends, so abortion is monotone: if a scan
// over prefix P aborted, the over-long line sits at a fixed offset in P with
// fixed bytes (or is a trailing partial line already past 64 KiB, which
// appending can only lengthen). Every scan over any superset of P reaches the
// same line and aborts identically, so waiting out the deadline could not change
// the answer.
//
// The only shared state is the mutex-guarded buffer, whose Bytes() returns a
// copy — so the scan always runs over a private snapshot and never reads memory
// the copier goroutine is appending to.
func trailWaitForTrailer(stdout *probeSyncBuffer, timeout time.Duration) trailObservation {
	start := time.Now()
	deadline := start.Add(timeout)
	// The zero instant means NO non-matching poll has been observed yet, which
	// is the state that makes a bound unavailable rather than merely small.
	var lastMiss time.Time

	for {
		now := time.Now()
		res := trailScan(stdout.Bytes())

		switch res.State {
		case trailSeen:
			obs := trailObservation{trailScanResult: res, ObservedAt: now}
			if lastMiss.IsZero() {
				obs.Staleness = now.Sub(start)
				obs.BoundFrom = trailBoundFromStart
			} else {
				obs.Staleness = now.Sub(lastMiss)
				obs.BoundFrom = trailBoundFromMiss
			}
			return obs
		case trailAborted:
			return trailObservation{trailScanResult: res, ObservedAt: now, BoundFrom: trailBoundNone}
		}

		lastMiss = now
		if !now.Before(deadline) {
			return trailObservation{trailScanResult: res, ObservedAt: now, BoundFrom: trailBoundNone}
		}
		time.Sleep(probePollInterval)
	}
}

// --- fixtures ----------------------------------------------------------------

// trailFixtureTrailer is one ordinary trailer in emitter.go:456-468's pinned
// wire order, carrying the values wireFields (emitter.go:428-437) renders for a
// clean completion. Short enough to survive the cap intact, which is what lets
// the seen rows assert Line verbatim.
const trailFixtureTrailer = `{"type":"result","subtype":"success","is_error":false,` +
	`"duration_ms":4210,"num_turns":3,"result":"done","stop_reason":"end_turn",` +
	`"session_id":"11111111-2222-3333-4444-555555555555","total_cost_usd":0.0123,` +
	`"usage":{"input_tokens":120,"output_tokens":45,"cache_creation_input_tokens":0,` +
	`"cache_read_input_tokens":0},"terminal_reason":"completed"}`

// trailFixtureNoTrailer is ordinary stream-json with no trailer in it, including
// a non-JSON line and a partial trailing line mid-write. Both are ordinary
// input: they fail to unmarshal and are simply not a match.
const trailFixtureNoTrailer = `{"type":"system","subtype":"init","cwd":"/tmp/wd","tools":["Bash"],"model":"claude-haiku-4-5","session_id":"11111111-2222-3333-4444-555555555555"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}
claude: warning on a line that is not json at all
{"type":"user","message":{"role":"user","content":"go"}}
{"type":"resu`

// trailNeedle stands in for the operator-visible text a real trailer's `result`
// field carries verbatim. It is placed PAST the cap so a record that leaked it
// could only have done so by recording the line in full.
const trailNeedle = "TRAIL-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE"

// trailPaddedTrailer renders a max_turns trailer whose `result` field carries
// pad bytes of padding followed by trailNeedle, with the wire order and every
// other field intact. terminal_reason is LAST on that order, so it is the first
// casualty of any cap applied to the line — which is the point.
func trailPaddedTrailer(pad int) string {
	return `{"type":"result","subtype":"error_max_turns","is_error":true,` +
		`"duration_ms":9001,"num_turns":6,"result":"` + strings.Repeat("x", pad) + trailNeedle + `",` +
		`"stop_reason":"end_turn","session_id":"11111111-2222-3333-4444-555555555555",` +
		`"total_cost_usd":0.42,"usage":{"input_tokens":120,"output_tokens":45,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0},` +
		`"terminal_reason":"max_turns"}`
}

// trailOverlongPad pushes a line past bufio.Scanner's 64 KiB default with room
// to spare, so the abort does not depend on the fixture's scaffolding length.
const trailOverlongPad = 70000

// --- tests -------------------------------------------------------------------

// TestTrailConstantsAreClosed is AC1's structural claim made executable. It
// fails the moment someone "simplifies" trailBoundNone to "": the whole point of
// the third bound-origin value is that a record carrying no bound cannot read as
// one bounded by a non-matching poll, and a zero-valued discriminator would read
// as neither and as everything.
func TestTrailConstantsAreClosed(t *testing.T) {
	closed := func(t *testing.T, space string, values map[string]string) {
		t.Helper()
		byValue := make(map[string]string, len(values))
		for name, value := range values {
			if value == "" {
				t.Errorf("%s: %s is the empty string, so a zero-valued field reads as it — the "+
					"defect the closed space exists to prevent", space, name)
				continue
			}
			if prev, dup := byValue[value]; dup {
				t.Errorf("%s: %s and %s both carry %q, so the two are indistinguishable in a "+
					"record", space, prev, name, value)
				continue
			}
			byValue[value] = name
		}
	}

	states := map[string]string{
		"trailSeen":    trailSeen,
		"trailAbsent":  trailAbsent,
		"trailAborted": trailAborted,
	}
	origins := map[string]string{
		"trailBoundFromMiss":  trailBoundFromMiss,
		"trailBoundFromStart": trailBoundFromStart,
		"trailBoundNone":      trailBoundNone,
	}
	closed(t, "scan states", states)
	closed(t, "bound origins", origins)

	// The same claim read off the record itself: an observation nobody filled in
	// must be invalid, never a reading. A run whose trailer never appeared is a
	// distinct observation from one that was never taken.
	var zero trailObservation
	for name, value := range states {
		if zero.State == value {
			t.Errorf("the zero trailObservation reads as %s (%q)", name, value)
		}
	}
	for name, value := range origins {
		if zero.BoundFrom == value {
			t.Errorf("the zero trailObservation's bound reads as %s (%q)", name, value)
		}
	}
}

func TestTrailScan(t *testing.T) {
	tests := []struct {
		name         string
		stdout       string
		wantState    string
		wantLine     string
		wantTrailer  *resultTrailer
		wantDetailIn []string
	}{
		{
			name:      "an ordinary trailer is seen, decoded and recorded whole",
			stdout:    trailFixtureTrailer + "\n",
			wantState: trailSeen,
			wantLine:  trailFixtureTrailer,
			wantTrailer: &resultTrailer{
				Type: "result", Subtype: "success", StopReason: "end_turn",
				NumTurns: 3, IsError: false, TerminalReason: "completed",
				Usage: resultTrailerUsage{InputTokens: 120, OutputTokens: 45},
			},
		},
		{
			name:      "a trailer preceded by ordinary stream-json is still the match",
			stdout:    trailFixtureNoTrailer + "\n" + trailFixtureTrailer + "\n",
			wantState: trailSeen,
			wantLine:  trailFixtureTrailer,
			wantTrailer: &resultTrailer{
				Type: "result", Subtype: "success", StopReason: "end_turn",
				NumTurns: 3, IsError: false, TerminalReason: "completed",
				Usage: resultTrailerUsage{InputTokens: 120, OutputTokens: 45},
			},
		},
		{
			// The control AC5 requires: without it, "aborted" could be the answer
			// to everything and the abort row below would prove nothing.
			name:         "ordinary stream-json with no trailer is absent, never aborted",
			stdout:       trailFixtureNoTrailer,
			wantState:    trailAbsent,
			wantDetailIn: []string{"no type:result line", "not an unreadable buffer"},
		},
		{
			name:         "empty stdout is absent",
			stdout:       "",
			wantState:    trailAbsent,
			wantDetailIn: []string{"no type:result line"},
		},
		{
			// The state parseResultTrailer collapses into its absence error.
			name:         "a trailer line past bufio.Scanner's 64 KiB default aborts, never absent",
			stdout:       trailPaddedTrailer(trailOverlongPad) + "\n",
			wantState:    trailAborted,
			wantDetailIn: []string{"token too long", trailAborted, trailAbsent},
		},
		{
			// Pins the early-return-before-scanner.Err() ordering: the scan had
			// already answered, so the tail was never read.
			name:      "an over-long line AFTER the trailer never changes the answer",
			stdout:    trailFixtureTrailer + "\n" + trailPaddedTrailer(trailOverlongPad) + "\n",
			wantState: trailSeen,
			wantLine:  trailFixtureTrailer,
			wantTrailer: &resultTrailer{
				Type: "result", Subtype: "success", StopReason: "end_turn",
				NumTurns: 3, IsError: false, TerminalReason: "completed",
				Usage: resultTrailerUsage{InputTokens: 120, OutputTokens: 45},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trailScan([]byte(tc.stdout))

			if got.State != tc.wantState {
				t.Fatalf("state: got %q (%s), want %q", got.State, got.Detail, tc.wantState)
			}
			if got.Line != tc.wantLine {
				t.Errorf("line: got %q, want %q", got.Line, tc.wantLine)
			}
			if got.Detail == "" {
				t.Error("empty detail: a result that cannot say which arm fired and why is " +
					"indistinguishable from a reading")
			}
			for _, want := range tc.wantDetailIn {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("detail: got %q, want it to contain %q", got.Detail, want)
				}
			}

			if tc.wantTrailer == nil {
				if got.Trailer != nil {
					t.Fatalf("trailer: got %+v, want nil — only %s carries a decode",
						got.Trailer, trailSeen)
				}
				return
			}
			if got.Trailer == nil {
				t.Fatalf("trailer: got nil, want %+v", tc.wantTrailer)
			}
			if *got.Trailer != *tc.wantTrailer {
				t.Errorf("trailer: got %+v, want %+v", *got.Trailer, *tc.wantTrailer)
			}
		})
	}

	t.Run("a trailer whose result field pushes the line past the cap", func(t *testing.T) {
		// `result` is sixth on the pinned wire order and terminal_reason last, so
		// a 512-byte cap applied to the LINE truncates inside the assistant
		// message and destroys the field #1267 branches on. Decoding the full
		// line and capping only the copy is what keeps both properties.
		line := trailPaddedTrailer(2000)
		got := trailScan([]byte(line + "\n"))

		if got.State != trailSeen {
			t.Fatalf("state: got %q (%s), want %q", got.State, got.Detail, trailSeen)
		}
		if want := reachMaxCommandBytes + len(reachTruncationMarker); len(got.Line) != want {
			t.Errorf("recorded line: got %d bytes, want %d — the line must be capped, and the "+
				"whole %d-byte line must never be recorded", len(got.Line), want, len(line))
		}
		if !strings.HasSuffix(got.Line, reachTruncationMarker) {
			t.Errorf("recorded line: got %q, want it to end in %q so a reader knows it is partial",
				got.Line, reachTruncationMarker)
		}
		if got.Trailer == nil {
			t.Fatal("trailer: got nil, want a decode of the full line")
		}
		if got.Trailer.TerminalReason != "max_turns" {
			t.Errorf("terminal_reason: got %q, want %q — it is LAST on the wire, ~2 KiB past the "+
				"cap, and the field the consumer branches on; a decode run against the capped "+
				"line could not have recovered it", got.Trailer.TerminalReason, "max_turns")
		}
		if !got.Trailer.IsError || got.Trailer.Subtype != "error_max_turns" {
			t.Errorf("subtype/is_error: got %q/%t, want %q/true — the budget-fired reading must "+
				"survive the cap too", got.Trailer.Subtype, got.Trailer.IsError,
				"error_max_turns")
		}

		// The leak assertion, and the executable form of "the decoded value
		// structurally cannot carry the assistant payload": the needle sits
		// ~2 KiB into the `result` field, so its only route into the record is
		// Line, and Line is capped.
		if strings.Contains(got.Line, trailNeedle) {
			t.Error("the recorded line carries the needle from past the cap")
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshalling the scan result: %v", err)
		}
		if bytes.Contains(encoded, []byte(trailNeedle)) {
			t.Errorf("the marshalled record carries verbatim model output from past the cap: %s",
				encoded)
		}
	})
}

func TestTrailWaitForTrailer(t *testing.T) {
	t.Run("a trailer appended mid-poll is bounded by the last non-matching poll", func(t *testing.T) {
		buf := &probeSyncBuffer{}

		var obs trailObservation
		done := make(chan struct{})
		go func() {
			defer close(done)
			obs = trailWaitForTrailer(buf, 30*time.Second)
		}()

		// Past two poll ticks, so at least one non-matching poll is certainly
		// observed before the append.
		time.Sleep(500 * time.Millisecond)

		// Stamped BEFORE the write, so the lateness the assertion computes is
		// slightly LARGER than the truth — making the assertion strictly harder
		// than the invariant it guards, and unable to pass by luck.
		appendAt := time.Now()
		if _, err := buf.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
			t.Fatalf("appending the trailer: %v", err)
		}
		<-done

		if obs.State != trailSeen {
			t.Fatalf("state: got %q (%s), want %q", obs.State, obs.Detail, trailSeen)
		}
		if obs.BoundFrom != trailBoundFromMiss {
			t.Fatalf("bound origin: got %q, want %q — a non-matching poll was observed before "+
				"the append, so the bound is a real one", obs.BoundFrom, trailBoundFromMiss)
		}
		if lateness := obs.ObservedAt.Sub(appendAt); obs.Staleness < lateness {
			t.Errorf("staleness %v does not cover the true lateness %v — a bound that undershoots "+
				"is a non-bound wearing a bound's label", obs.Staleness, lateness)
		}
		if obs.Trailer == nil {
			t.Fatal("trailer: got nil — the record must be usable at the instant it reports")
		}
		if obs.Trailer.TerminalReason != "completed" {
			t.Errorf("terminal_reason: got %q, want %q", obs.Trailer.TerminalReason, "completed")
		}
	})

	t.Run("a trailer already in the buffer reports a start-derived bound", func(t *testing.T) {
		buf := &probeSyncBuffer{}
		if _, err := buf.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
			t.Fatalf("pre-filling the buffer: %v", err)
		}

		obs := trailWaitForTrailer(buf, 30*time.Second)

		if obs.State != trailSeen {
			t.Fatalf("state: got %q (%s), want %q", obs.State, obs.Detail, trailSeen)
		}
		// The duration is real but bounds nothing: the trailer may have become
		// visible long before the loop began. A consumer tells this apart from
		// the subtest above by this discriminator alone.
		if obs.BoundFrom != trailBoundFromStart {
			t.Errorf("bound origin: got %q, want %q — no non-matching poll was ever observed",
				obs.BoundFrom, trailBoundFromStart)
		}
		if obs.Staleness < 0 {
			t.Errorf("staleness: got %v, want a non-negative duration", obs.Staleness)
		}
		if obs.ObservedAt.IsZero() {
			t.Error("observed_at: got the zero instant, want the poll's stamp")
		}
	})

	t.Run("no trailer before the deadline reports an absence with no bound", func(t *testing.T) {
		buf := &probeSyncBuffer{}

		obs := trailWaitForTrailer(buf, 600*time.Millisecond)

		if obs.State != trailAbsent {
			t.Fatalf("state: got %q (%s), want %q", obs.State, obs.Detail, trailAbsent)
		}
		if obs.BoundFrom != trailBoundNone {
			t.Errorf("bound origin: got %q, want %q — nothing was bounded, and reporting a "+
				"non-matching-poll bound here would be a duration standing in for an absence",
				obs.BoundFrom, trailBoundNone)
		}
		if obs.Staleness != 0 {
			t.Errorf("staleness: got %v, want 0 — no bound was measured", obs.Staleness)
		}
		if obs.Trailer != nil {
			t.Errorf("trailer: got %+v, want nil", obs.Trailer)
		}
	})
}
