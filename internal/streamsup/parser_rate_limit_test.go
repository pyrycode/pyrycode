package streamsup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// rateLimitFieldCapFixture records maxRateLimitField as a LITERAL, deliberately
// not as the constant, for taskStartedCapCheat's reason: a fixture built from the
// constant it validates asserts nothing about the number — halve the constant and
// every cap row below would follow it green.
const rateLimitFieldCapFixture = 256

// rateLimitBenignFixture is claude's measured-benign status as a LITERAL, for
// harnessNudgeFixture's reason. The whole gate turns on this one value, so a
// fixture written as benignRateLimitStatus would follow a rename or a
// recapitalisation of the constant green — which is precisely the edit the
// byte-exact match exists to make visible.
const rateLimitBenignFixture = "allowed"

// rateLimitDropMsgFixture is the Debug message emitRateLimit's drop site emits, as
// a literal for the same reason.
const rateLimitDropMsgFixture = "streamsup: dropping rate_limit_event"

// rateLimitLineFixture builds a rate_limit_event line from the three mapped keys.
// It invents NO field structure — the keys are exactly the captures' — and exists
// only to vary the VALUES, which is what the gate and cap proofs need and what the
// captures cannot supply: all three carry the benign status, and every value in
// them is under ten bytes.
func rateLimitLineFixture(t *testing.T, status, limitType string, resetsAt int64) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": "rate_limit_event",
		"rate_limit_info": map[string]any{
			"status":        status,
			"rateLimitType": limitType,
			"resetsAt":      resetsAt,
		},
	})
	if err != nil {
		t.Fatalf("marshalling rate_limit_event fixture: %v", err)
	}
	return string(b)
}

// rateLimitEvent drives one line through the shipped parser and returns the single
// turnevent.RateLimited it must emit.
func rateLimitEvent(t *testing.T, line string) turnevent.RateLimited {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.RateLimited)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.RateLimited", got[0])
	}
	return ev
}

// TestParser_RateLimitCapturedBenignLineIsSilent is #1404's rung-1 assertion, and
// it is a REGRESSION test rather than an example: the captured line is what claude
// actually puts on the wire once per run, its status is the measured-benign value,
// and every healthy run must therefore produce nothing at all. A parser that
// mapped this line 1:1 would put one "you are rate limited" event on every turn.
//
// The two failure modes are called out separately even though a zero-event check
// covers both, because they mean different things: a RateLimited here says the
// gate is gone, an Unrecognized here says the type stopped being claimed by
// consumeLine's own case arm.
func TestParser_RateLimitCapturedBenignLineIsSilent(t *testing.T) {
	t.Parallel()
	line := capturedLine(t, "rate_limit_event", "")

	// The premise, checked rather than assumed: if the capture's status were
	// anything but the benign value, this test would pin the wrong rung and pass for
	// the wrong reason.
	var payload struct {
		Info struct {
			Status string `json:"status"`
		} `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	if payload.Info.Status != rateLimitBenignFixture {
		t.Fatalf("the captured line's rate_limit_info.status is %q, want %q — this test pins rung 1 and the capture no longer carries it",
			payload.Info.Status, rateLimitBenignFixture)
	}

	got := collectEvents(string(line))
	for _, ev := range got {
		switch ev.(type) {
		case turnevent.RateLimited:
			t.Errorf("the captured rate_limit_event produced %#v; its status is the measured-benign value, so a healthy run must emit no RateLimited", ev)
		case turnevent.Unrecognized:
			t.Errorf("the captured rate_limit_event produced %#v; the type is claimed by consumeLine's own case arm and must never reach the unrecognized lane", ev)
		}
	}
	if len(got) != 0 {
		t.Errorf("event count: got %d, want 0 — %#v", len(got), got)
	}

	// The control, and it is load-bearing for TestDropcapClassification's reason:
	// without a line that must NOT be silent, the zero-event assertion above passes
	// vacuously against a mis-wired sink or a parser that consumes the line before
	// ever reaching the gate.
	if got := collectEvents(rateLimitLineFixture(t, "exceeded", "five_hour", 1785699000)); len(got) != 1 {
		t.Fatalf("control: a non-benign rate_limit_event emitted %d events, want 1 — the silence above proves nothing", len(got))
	}
}

// TestParser_RateLimitEmitsForNonBenignStatus pins rung 2: anything that is not the
// one measured-benign status becomes exactly one turnevent.RateLimited carrying
// claude's own values.
//
// The value set beyond "allowed" is ALMOST entirely unmeasured — no capture of a
// limit actually in force exists — so the hypothetical rows deliberately do not pin
// one invented alternative. Two different non-benign values are what assert
// "anything but the benign value", and the case-differing row is the byte-exact
// match made visible.
//
// ONE row is not hypothetical. "allowed_warning" was observed live on 2026-08-22
// (claude 2.1.239, limit_type "seven_day"): the account sat inside its weekly
// warning band and every turn still ran. It is a NEW SIBLING of the benign value
// rather than a rename of it, which is why benignRateLimitStatus is unchanged and
// this row asserts the frame FIRES. Warning ahead of the wall is the moment the
// mapping is worth most, so suppressing it here would leave the event useful only
// once the user is already blocked. The live tier that first saw it tolerates it by
// name (internal/e2e/realclaude's warnRateLimitStatus); this row is the offline
// half, so the observation survives without a claude login and without the account
// happening to be near its ceiling again.
func TestParser_RateLimitEmitsForNonBenignStatus(t *testing.T) {
	t.Parallel()
	const (
		limitType = "five_hour"
		resetsAt  = int64(1785699000)
	)
	tests := []struct {
		name   string
		status string
		why    string
	}{
		{
			name: "the MEASURED warning-band status emits", status: "allowed_warning",
			why: "observed live 2026-08-22 on claude 2.1.239 against limit_type seven_day — the only " +
				"non-benign value on record, and the one row here that is not hypothetical. It is a " +
				"new sibling of the benign value, not a rename, so it must EMIT: a phone that only " +
				"hears about the usage window once the wall is hit cannot act on it",
		},
		{
			name: "a plausible limited status emits", status: "exceeded",
			why: "the direction the gate exists for",
		},
		{
			name: "a second, different non-benign status emits", status: "rejected",
			why: "pins `anything but the benign value` rather than one hardcoded alternative",
		},
		{
			name: "a status differing from the benign value only in case emits", status: "Allowed",
			why: "benignRateLimitStatus is matched byte-exact — no fold, no trim, no prefix. This row " +
				"is the accepted failure direction made visible: a claude that recapitalises the " +
				"benign value makes this event fire once per healthy run, loudly and wrongly, which " +
				"is one constant edit to fix, where a folded match would suppress a real limit in silence",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev := rateLimitEvent(t, rateLimitLineFixture(t, tt.status, limitType, resetsAt))
			if ev.Status != tt.status {
				t.Errorf("Status: got %q, want claude's value %q verbatim (%s)", ev.Status, tt.status, tt.why)
			}
			if ev.LimitType != limitType {
				t.Errorf("LimitType: got %q, want the captures' rateLimitType %q", ev.LimitType, limitType)
			}
			if ev.ResetsAt != resetsAt {
				t.Errorf("ResetsAt: got %d, want claude's resetsAt %d", ev.ResetsAt, resetsAt)
			}
			if ev.TruncatedFields != nil {
				t.Errorf("TruncatedFields: got %v, want nil — every value here is far under the cap", ev.TruncatedFields)
			}
		})
	}

	t.Run("a non-benign status with no detail still emits", func(t *testing.T) {
		t.Parallel()
		// Absence of the detail is claude's to choose and the status IS the report,
		// so a missing rateLimitType/resetsAt lands empty and 0 rather than becoming
		// a validation rule. Written as a raw line rather than through the fixture
		// because the fixture always supplies all three keys.
		ev := rateLimitEvent(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"exceeded"}}`)
		if ev.Status != "exceeded" || ev.LimitType != "" || ev.ResetsAt != 0 {
			t.Errorf("got %#v, want Status \"exceeded\" with LimitType empty and ResetsAt 0", ev)
		}
	})
}

// TestParser_RateLimitSilentRungsLogTheirReason pins every silent rung, and it pins
// the logged REASON as well as the silence. Three rungs that all produce zero
// events are otherwise indistinguishable from one another and from a dead arm, so
// the reason is what separates "silent for the right rung" from "silent by
// accident".
//
// The rung-3 rows are AC2 case (c) — a line carrying no decodable rate_limit_info
// — which #1404 had to DECIDE rather than fall into. Every rate_limit_event shape
// this repo's tests carried before #1404 had exactly that shape, which is why the
// decision moved those rows rather than the other way round; two of them are here
// verbatim (the `rate_limit` wrong-container line from
// TestParser_IgnoredLineTypesStaySilent, and the `retry_after` line still in
// TestParser_LineMapping).
func TestParser_RateLimitSilentRungsLogTheirReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		line       string
		wantReason string
		why        string
	}{
		{
			name: "rung 1: the measured-benign status", line: rateLimitLineFixture(t, rateLimitBenignFixture, "five_hour", 1785699000),
			wantReason: rateLimitDropBenign,
			why:        "claude reports the window once per run whatever its state; this is the healthy one",
		},
		{
			name: "rung 3: no container at all", line: `{"type":"rate_limit_event"}`,
			wantReason: rateLimitDropNoInfo,
			why:        "the shape the realclaude mirror and dropcap rows drive",
		},
		{
			name: "rung 3: the wrong container key", line: `{"type":"rate_limit_event","rate_limit":{"status":"allowed"}}`,
			wantReason: rateLimitDropNoInfo,
			why:        "claude's key is rate_limit_info; a status under any other key was never reported",
		},
		{
			name: "rung 3: an unrelated payload", line: `{"type":"rate_limit_event","retry_after":10}`,
			wantReason: rateLimitDropNoInfo,
			why:        "there is no retry_after in anything claude sends on this line",
		},
		{
			name: "rung 3: container present, status absent", line: `{"type":"rate_limit_event","rate_limit_info":{}}`,
			wantReason: rateLimitDropNoInfo,
			why:        "an empty container and an absent one are answered identically, which is what makes the plain-struct decode target sufficient",
		},
		{
			name: "the container is not an object", line: `{"type":"rate_limit_event","rate_limit_info":"nope"}`,
			wantReason: rateLimitDropUndecodable,
			why:        "the WHOLE-LINE decode fails, so this takes the undecodable arm — same visible outcome as rung 3, different logged reason",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
			if _, err := p.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}

			if len(events) != 0 {
				t.Errorf("event count: got %d, want 0 (%s) — %#v", len(events), tt.why, events)
			}
			drops := rec.withMessage(rateLimitDropMsgFixture)
			if len(drops) != 1 {
				t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)", rateLimitDropMsgFixture, len(drops), rec.all())
			}
			wantAttrs := map[string]string{"reason": tt.wantReason}
			if !reflect.DeepEqual(drops[0].attrs, wantAttrs) {
				t.Errorf("drop attrs: got %v, want exactly %v (%s)", drops[0].attrs, wantAttrs, tt.why)
			}
		})
	}

	// The control: without a line that must NOT be silent, every assertion above
	// passes against a parser whose arm does nothing at all.
	t.Run("control_a_non_benign_line_emits_and_logs_no_drop", func(t *testing.T) {
		t.Parallel()
		rec := &logRecorder{}
		var events []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
		if _, err := p.Write([]byte(rateLimitLineFixture(t, "exceeded", "five_hour", 1785699000) + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		if len(events) != 1 {
			t.Fatalf("event count: got %d, want 1 — the silent rows above prove nothing", len(events))
		}
		if drops := rec.withMessage(rateLimitDropMsgFixture); len(drops) != 0 {
			t.Errorf("the emit path logged %d drop record(s), want 0: %+v", len(drops), drops)
		}
	})
}

// TestParser_RateLimitFallingEdgeLatchIsPerParser pins #2250's gate as a state
// machine, over the sequences no capture holds and none can be made to.
//
// The composed-capture fixture (rate_limit_capture_test.go) proves the edge fires
// over claude's own bytes; what it cannot express is what happens when a line the
// gate SILENCES sits between the two readings, because no committed record carries a
// rate_limit_event with an unusable container. Those rows are the ones that decide
// whether the latch is written where a reader would expect: a rung that returns for
// want of a report must leave the remembered reading alone, or a single malformed
// line swallows the clear and the client's banner sticks anyway — the defect this
// ticket exists to fix, restored one shape further in.
//
// Asserting the STATUSES IN ORDER rather than a count: a table that only counted
// would pass on an implementation that emitted the warning twice and the clear never.
func TestParser_RateLimitFallingEdgeLatchIsPerParser(t *testing.T) {
	t.Parallel()

	const (
		limitType = "five_hour"
		resetsAt  = int64(1785699000)
		nonBenign = "exceeded"
	)
	warn := rateLimitLineFixture(t, nonBenign, limitType, resetsAt)
	benign := rateLimitLineFixture(t, rateLimitBenignFixture, limitType, resetsAt)

	tests := []struct {
		name  string
		lines []string
		want  []string
		why   string
	}{
		{
			name:  "a parser that only ever reports the benign status stays silent",
			lines: []string{benign, benign, benign},
			want:  nil,
			why: "the load-bearing silence: claude reports the window once per run whatever its " +
				"state, so a healthy run must still emit nothing at all",
		},
		{
			name:  "the falling edge fires once and then stops",
			lines: []string{warn, benign, benign, benign},
			want:  []string{nonBenign, rateLimitBenignFixture},
			why: "further benign readings after the clear produce nothing until another " +
				"non-benign one arrives — a client needs one frame to take a banner down, not one per run",
		},
		{
			name:  "a turn boundary does not clear the latch",
			lines: []string{warn, `{"type":"result","subtype":"success"}`, benign},
			want:  []string{nonBenign, rateLimitBenignFixture},
			why: "consumeLine's one reset point deliberately does NOT clear this field. Cleared " +
				"there it would be gone before the reading that needs it arrives: claude emits this " +
				"line once per run and the parser outlives the run",
		},
		{
			name:  "a reading with no usable rate_limit_info leaves the latch alone",
			lines: []string{warn, `{"type":"rate_limit_event"}`, benign},
			want:  []string{nonBenign, rateLimitBenignFixture},
			why: "the rung that returns for want of a report returns BEFORE the switch can write, " +
				"so a container claude renamed or dropped cannot swallow the clear",
		},
		{
			name:  "an undecodable line leaves the latch alone",
			lines: []string{warn, `{"type":"rate_limit_event","rate_limit_info":"nope"}`, benign},
			want:  []string{nonBenign, rateLimitBenignFixture},
			why: "the undecodable arm returns before the decode's result is ever read, one rung " +
				"earlier than the row above and for the same reason",
		},
		{
			name:  "a second non-benign reading re-arms it",
			lines: []string{warn, benign, warn, benign},
			want:  []string{nonBenign, rateLimitBenignFixture, nonBenign, rateLimitBenignFixture},
			why: "the edge is a pair, not a one-shot: a session that crosses the band twice must " +
				"be able to clear the banner twice",
		},
		{
			name:  "the clear is not owed to a status that merely differs in case",
			lines: []string{rateLimitLineFixture(t, "Allowed", limitType, resetsAt), benign},
			want:  []string{"Allowed", rateLimitBenignFixture},
			why: "benignRateLimitStatus is matched byte-exact, so a recapitalised value opens the " +
				"latch like any other non-benign reading rather than being folded into the silent rung",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, ev := range collectEventsFromLines(tt.lines) {
				if rl, ok := ev.(turnevent.RateLimited); ok {
					got = append(got, rl.Status)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("statuses in order: got %v, want %v (%s)", got, tt.want, tt.why)
			}
		})
	}

	// Per-parser and not per-package, which is the property a /clear rotation makes
	// observable: a rotation or an eviction mints a new parser, so a warning shown
	// before one is not cleared after it. Named as a limitation on the wire rather
	// than engineered around, and pinned here so the day it changes, this reddens.
	t.Run("a fresh parser does not inherit another's open latch", func(t *testing.T) {
		t.Parallel()
		if got := collectEventsFromLines([]string{warn}); len(got) != 1 {
			t.Fatalf("the first parser emitted %d events, want 1 — the second's silence proves nothing", len(got))
		}
		for _, ev := range collectEventsFromLines([]string{benign}) {
			if rl, ok := ev.(turnevent.RateLimited); ok {
				t.Errorf("a fresh parser emitted %#v for a benign reading; the latch is per-parser state "+
					"and a new one starts closed", rl)
			}
		}
	})
}

// rateLimitUtilizationLineFixture builds a non-benign rate_limit_event line whose
// utilization term is the RAW JSON text given, with the empty string meaning the key
// is omitted entirely.
//
// A raw fragment rather than a fourth parameter on rateLimitLineFixture, and the
// reason is not tidiness: two of the rows this serves cannot be expressed through
// map[string]any at all. A JSON string where a number belongs needs the bytes
// `"0.94"`, and a number outside float64's range needs `1e400`, which json.Marshal
// refuses to write from any Go value because the nearest one is +Inf. Building the
// line as text is what lets the undecodable rows be exactly the bytes a hostile or
// changed claude would send. It invents no field structure: the four keys are the
// captures' own.
func rateLimitUtilizationLineFixture(status, limitType string, resetsAt int64, utilization string) string {
	info := fmt.Sprintf(`"status":%q,"rateLimitType":%q,"resetsAt":%d`, status, limitType, resetsAt)
	if utilization != "" {
		info += `,"utilization":` + utilization
	}
	return `{"type":"rate_limit_event","rate_limit_info":{` + info + `}}`
}

// TestParser_RateLimitCarriesUtilizationUnvalidated is #2249's AC 4 at the producer:
// claude's reading crosses verbatim, and nothing about it is clamped, rounded or
// range-checked.
//
// THE FIRST THREE ROWS ARE THE POINT AND THEY SIT ADJACENT DELIBERATELY, which is
// TestParser_CompactBoundaryPublishesTriggerAndCounts' arrangement and its reason: a
// plain float64 in place of the pointer passes the absent row (nil reads as 0) while
// failing the explicit-zero row, or the other way round depending on how the
// assertion is written, so neither row alone decides the shape. Only the pair does.
// An absent key and an explicit null are one reading because nothing downstream
// answers them differently — claude stating nothing and claude stating null are the
// same fact.
//
// The two out-of-range rows are not hypotheticals about claude so much as the stated
// contract: the field is NOT a bounded fraction, and a consumer that scales a gauge
// by it without a range check is the realistic bug. A clamp added here would make
// that bug invisible rather than fixing it, and would destroy the evidence of what
// claude actually sent. 0.94 is the one observed value and is the capture's; it is
// pinned against the committed bytes in rate_limit_capture_test.go rather than here,
// so this row asserts the hermetic path agrees with that one.
func TestParser_RateLimitCarriesUtilizationUnvalidated(t *testing.T) {
	t.Parallel()

	// Addressable locals rather than a generic pointer helper, which is
	// TestParser_CompactBoundaryPublishesTriggerAndCounts' idiom in this package for
	// the same absent-versus-present table.
	zero, observed, negative, aboveOne := 0.0, 0.94, -3.5, 17.25

	tests := []struct {
		name        string
		utilization string
		want        *float64
		why         string
	}{
		{
			name: "the key is absent", utilization: "", want: nil,
			why: "every committed record carrying the benign status omits it, so absence is the " +
				"measured majority case rather than an edge",
		},
		{
			name: "an explicit zero is a reading, not an absence", utilization: "0", want: &zero,
			why: "a fresh window is a real reading; folding it into nil would present an untouched " +
				"quota as an exhausted one, which is exactly the failure the pointer prevents",
		},
		{
			name: "an explicit null reads as absent", utilization: "null", want: nil,
			why: "claude stating nothing and claude stating null are one fact and nothing answers " +
				"them differently",
		},
		{
			name: "the observed warning-band reading", utilization: "0.94", want: &observed,
			why: "the value the committed allowed_warning capture carries, asserted here on the " +
				"hermetic path too",
		},
		{
			name: "a negative reading crosses verbatim", utilization: "-3.5", want: &negative,
			why: "claude's number, unvalidated in BOTH directions — a clamp to 0 would invent a " +
				"reading claude did not make",
		},
		{
			name: "a reading above one crosses verbatim", utilization: "17.25", want: &aboveOne,
			why: "not a bounded fraction: 17.25 is not rejected, not divided by 100, and not " +
				"treated as a percentage",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			line := rateLimitUtilizationLineFixture("exceeded", "five_hour", 1785699000, tt.utilization)
			ev := rateLimitEvent(t, line)

			switch {
			case tt.want == nil && ev.Utilization != nil:
				t.Errorf("Utilization: got %v, want nil (%s)", *ev.Utilization, tt.why)
			case tt.want != nil && ev.Utilization == nil:
				t.Errorf("Utilization: got nil, want %v (%s)", *tt.want, tt.why)
			case tt.want != nil && *ev.Utilization != *tt.want:
				t.Errorf("Utilization: got %v, want %v (%s)", *ev.Utilization, *tt.want, tt.why)
			}
			// The reading must not cost the rest of the mapping: a decode target that
			// mis-declared the new key could shift the others, and every row here would
			// still pass on the field it was written for.
			if ev.Status != "exceeded" || ev.LimitType != "five_hour" || ev.ResetsAt != 1785699000 {
				t.Errorf("the other three fields are %q/%q/%d, want exceeded/five_hour/1785699000",
					ev.Status, ev.LimitType, ev.ResetsAt)
			}
			// Still not a cut field and still not capped: a float64 cannot grow, which is
			// ResetsAt's reason for the same omission.
			if ev.TruncatedFields != nil {
				t.Errorf("TruncatedFields: got %v, want nil — utilization is not a member and "+
					"nothing here is near maxRateLimitField", ev.TruncatedFields)
			}
		})
	}
}

// TestParser_RateLimitUndecodableUtilizationDropsTheWholeLine is AC 4's other half:
// a reading this parser cannot read as a number takes emitRateLimit's EXISTING
// undecodable drop. No new reason, no new rung, no range check, and no partial event
// carrying the three fields that did decode.
//
// Dropping the whole line is the conservative direction and it is the same mechanism
// rateLimitInfo's doc already states for a non-numeric resetsAt: encoding/json fails
// the whole-line decode, so there is nothing to emit a half of. The out-of-range row
// matters on its own because the failure mode it rules out is silent — a decoder that
// saturated would hand the event +Inf, and +Inf is not JSON-encodable, so the frame
// would fail at the wire instead of here.
//
// The exact-attrs comparison is also the content-free logging assertion: a number is
// the value a drop site is most tempted to explain itself with, and an attrs map with
// anything beyond `reason` in it reddens here.
func TestParser_RateLimitUndecodableUtilizationDropsTheWholeLine(t *testing.T) {
	t.Parallel()

	// Discriminating on purpose. A needle of 0.94 could occur in an unrelated attr by
	// coincidence, which would make the no-leak sweep below pass or fail for reasons
	// having nothing to do with this field.
	const needle = "8675309"

	tests := []struct {
		name        string
		utilization string
		why         string
	}{
		{
			name: "a string where a number belongs", utilization: `"0.` + needle + `"`,
			why: "the WHOLE-LINE decode fails, exactly as it does for a non-numeric resetsAt",
		},
		{
			name: "a number outside float64's range", utilization: "1." + needle + "e400",
			why: "encoding/json refuses the conversion rather than saturating, so no non-finite " +
				"reading can reach the event",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
			line := rateLimitUtilizationLineFixture("exceeded", "five_hour", 1785699000, tt.utilization)
			if _, err := p.Write([]byte(line + "\n")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}

			if len(events) != 0 {
				t.Errorf("event count: got %d, want 0 (%s) — a partial event carrying the three "+
					"fields that did decode would report a limit whose reading the parser could "+
					"not read: %#v", len(events), tt.why, events)
			}
			drops := rec.withMessage(rateLimitDropMsgFixture)
			if len(drops) != 1 {
				t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)",
					rateLimitDropMsgFixture, len(drops), rec.all())
			}
			wantAttrs := map[string]string{"reason": rateLimitDropUndecodable}
			if !reflect.DeepEqual(drops[0].attrs, wantAttrs) {
				t.Errorf("drop attrs: got %v, want exactly %v — this field earns NO new drop "+
					"reason (%s)", drops[0].attrs, wantAttrs, tt.why)
			}
			for _, r := range rec.all() {
				for key, value := range r.attrs {
					if strings.Contains(value, needle) {
						t.Errorf("log record %q attr %q carries the undecodable reading (%q). "+
							"NOTHING from this payload is logged on any path", r.msg, key, value)
					}
				}
			}
		})
	}

	// The control: without a line whose utilization DOES decode, the rows above pass
	// against a parser that dropped every rate_limit_event for some other reason.
	t.Run("control_a_decodable_reading_emits_and_logs_no_drop", func(t *testing.T) {
		t.Parallel()
		rec := &logRecorder{}
		var events []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
		line := rateLimitUtilizationLineFixture("exceeded", "five_hour", 1785699000, "0."+needle)
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
		if len(events) != 1 {
			t.Fatalf("event count: got %d, want 1 — the drop rows above prove nothing", len(events))
		}
		if drops := rec.withMessage(rateLimitDropMsgFixture); len(drops) != 0 {
			t.Errorf("the emit path logged %d drop record(s), want 0: %+v", len(drops), drops)
		}
		// The emit path must not log the reading either, which the drop rows cannot say:
		// they never reach this branch.
		for _, r := range rec.all() {
			for key, value := range r.attrs {
				if strings.Contains(value, needle) {
					t.Errorf("log record %q attr %q carries claude's reading (%q) on the EMIT path",
						r.msg, key, value)
				}
			}
		}
	})
}

// TestParser_RateLimitFieldCaps pins the construction-time bound. It is applied
// before the event reaches the sink, so an oversized payload never enters the event
// stream, a queue, or a log — the same ordering maxUnrecognizedRaw's cap has, and
// the only thing that makes the bound real rather than cosmetic.
//
// Every row's status is non-benign on purpose: the gate runs BEFORE any cap, so a
// benign status would be swallowed and the row would prove nothing about the bound.
func TestParser_RateLimitFieldCaps(t *testing.T) {
	t.Parallel()
	over := strings.Repeat("x", rateLimitFieldCapFixture+10)
	const (
		shortStatus    = "exceeded"
		shortLimitType = "five_hour"
	)
	tests := []struct {
		name          string
		status        string
		limitType     string
		wantStatusLen int
		wantLimitLen  int
		wantCut       []string
	}{
		{
			name:   "nothing over the cap reports nil, not an empty slice",
			status: shortStatus, limitType: shortLimitType,
			wantStatusLen: len(shortStatus), wantLimitLen: len(shortLimitType),
			wantCut: nil,
		},
		{
			name:   "status over the cap is cut and named",
			status: over, limitType: shortLimitType,
			wantStatusLen: rateLimitFieldCapFixture, wantLimitLen: len(shortLimitType),
			wantCut: []string{"status"},
		},
		{
			name:   "limit_type over the cap is cut and named with the DAEMON's name",
			status: shortStatus, limitType: over,
			wantStatusLen: len(shortStatus), wantLimitLen: rateLimitFieldCapFixture,
			wantCut: []string{"limit_type"},
		},
		{
			name:   "both over the cap report in bound() call order",
			status: over, limitType: over,
			wantStatusLen: rateLimitFieldCapFixture, wantLimitLen: rateLimitFieldCapFixture,
			// The ORDER is the contract, not an accident: TruncatedFields is ordered by
			// emitRateLimit's sequential bound() calls.
			wantCut: []string{"status", "limit_type"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev := rateLimitEvent(t, rateLimitLineFixture(t, tt.status, tt.limitType, 1785699000))
			if len(ev.Status) != tt.wantStatusLen {
				t.Errorf("len(Status): got %d, want %d", len(ev.Status), tt.wantStatusLen)
			}
			if len(ev.LimitType) != tt.wantLimitLen {
				t.Errorf("len(LimitType): got %d, want %d", len(ev.LimitType), tt.wantLimitLen)
			}
			// DeepEqual and not a length check: it pins the ORDER, the NAMES, and the
			// nil-not-empty contract in one assertion.
			if !reflect.DeepEqual(ev.TruncatedFields, tt.wantCut) {
				t.Errorf("TruncatedFields: got %#v, want %#v", ev.TruncatedFields, tt.wantCut)
			}
			if ev.ResetsAt != 1785699000 {
				t.Errorf("ResetsAt: got %d, want 1785699000 — it carries no cap and must survive a cut on its neighbours", ev.ResetsAt)
			}
		})
	}
}

// TestParser_RateLimitIsLoggedContentFree is the assertion the per-rung `reason`
// checks do NOT cover: an implementation that sets reason correctly AND also logs
// "status", rl.Info.Status passes every one of them.
//
// It sweeps every record's message and every attribute value for the fixture's own
// distinctive values, on the EMIT path as well as the three drop rungs. Status is
// the field a drop site is most tempted to explain itself with, and the one value
// on this line a future claude could make arbitrarily long or arbitrarily
// revealing. Mirrors TestParser_HarnessNudgeDropIsLoggedContentFree.
//
// AMENDED 2026-09-10 (#2250): the sweep now covers FOUR paths, because it always
// drove one parser and the second line's benign reading now follows a non-benign one
// — which is rung 4, the falling edge. That path is the one a future edit is most
// likely to explain itself with, since a benign status that EMITS looks like a
// contradiction worth annotating, so it earns coverage here rather than a row of its
// own: benignLimitType and the benign status are already in the leak set below, and
// they now ride the emit path as well as a drop. The two counts moved with it and
// nothing else did.
func TestParser_RateLimitIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	const (
		emitStatus      = "rl-status-70141"
		emitLimitType   = "rl-limit-70143"
		benignLimitType = "rl-limit-70147"
		undecodableMark = "rl-undecodable-70149"
	)
	captured := capturedLine(t, "rate_limit_event", "")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	capturedUUID, _ := payload["uuid"].(string)
	if capturedSession == "" || capturedUUID == "" {
		t.Fatalf("the capture carries no string session_id/uuid, so half this sweep would be vacuous")
	}

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	lines := []string{
		// The emit path, first: it must produce the event and log nothing at all.
		rateLimitLineFixture(t, emitStatus, emitLimitType, 1785699000),
		// The SECOND emit path since #2250: a benign reading following the non-benign
		// one above is the falling edge, so this line emits and logs nothing either.
		rateLimitLineFixture(t, rateLimitBenignFixture, benignLimitType, 1785699000),
		// Benign with the latch now closed — the ordinary silent rung, and its
		// position after the edge is what makes it that rather than a second edge.
		string(captured),
		`{"type":"rate_limit_event","rate_limit_info":"` + undecodableMark + `"}`,
	}
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}

	if len(events) != 2 {
		t.Fatalf("event count: got %d, want 2 (the non-benign line, then the falling edge on the "+
			"benign line after it) — %#v", len(events), events)
	}
	for i, ev := range events {
		if _, ok := ev.(turnevent.RateLimited); !ok {
			t.Fatalf("event %d type: got %T, want turnevent.RateLimited", i, ev)
		}
	}
	// Two drops and not four: BOTH emit paths log nothing, which is the half of this
	// test the drop-rung assertions cannot see.
	if drops := rec.withMessage(rateLimitDropMsgFixture); len(drops) != 2 {
		t.Errorf("drop records: got %d, want 2 (one benign with the latch closed, one undecodable): %+v",
			len(drops), drops)
	}

	leaks := []string{
		emitStatus, emitLimitType, benignLimitType, undecodableMark,
		rateLimitBenignFixture, "five_hour", capturedSession, capturedUUID,
	}
	for _, r := range rec.all() {
		for _, leak := range leaks {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content (%q): %q", leak, r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content (%q); this path logs a daemon-authored reason keyword only",
						r.msg, k, leak)
				}
			}
		}
	}
}

// TestParser_RateLimitDropsSessionAndUUID sweeps the family's two standing drops off
// the emitted event, by REFLECTION over its string fields rather than over a list,
// so a field added to the event later is covered without anyone remembering to
// extend one.
//
// It cannot drive the captured line as-is: every capture on record carries the
// benign status, so the captured line emits NOTHING by design (rung 1). The line
// here is DERIVED from the capture — its status flipped to a non-benign value and
// every other key, uuid and session_id included, left exactly as claude sent them —
// which is what keeps this a statement about the real payload rather than about a
// hand-built one. Both values are read out of the capture rather than pinned: the
// capture is redacted (session_id reads $SESSION_ID), so a literal would pin the
// placeholder and stop being honest the day the capture is re-taken unredacted.
func TestParser_RateLimitDropsSessionAndUUID(t *testing.T) {
	t.Parallel()
	captured := capturedLine(t, "rate_limit_event", "")

	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	capturedUUID, _ := payload["uuid"].(string)
	if capturedSession == "" || capturedUUID == "" {
		t.Fatalf("the capture carries no string session_id/uuid, so the drop assertion would be vacuous")
	}
	info, ok := payload["rate_limit_info"].(map[string]any)
	if !ok {
		t.Fatalf("the capture's rate_limit_info is not a JSON object; the derivation below would silently change the shape under test")
	}
	if info["status"] != rateLimitBenignFixture {
		t.Fatalf("the capture's status is %v, want %q — the flip below assumes it starts benign", info["status"], rateLimitBenignFixture)
	}
	info["status"] = "exceeded"
	derived, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("re-marshalling the derived line: %v", err)
	}

	ev := rateLimitEvent(t, string(derived))
	// The canary: proves the derivation carried claude's own detail through rather
	// than producing some other shape that happens to emit.
	if want, _ := info["rateLimitType"].(string); ev.LimitType != want {
		t.Errorf("LimitType: got %q, want the capture's rateLimitType %q", ev.LimitType, want)
	}

	rv := reflect.ValueOf(ev)
	var swept int
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() != reflect.String {
				continue
			}
			swept++
			if rv.Field(i).String() == v {
				t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
					rv.Type().Field(i).Name, key, v)
			}
		}
	}
	// Two string fields times two keys. The sweep visiting nothing would pass
	// silently, which is the one way this assertion could rot into decoration.
	const wantSwept = 4
	if swept < wantSwept {
		t.Errorf("the drop sweep visited %d string fields, want at least %d", swept, wantSwept)
	}
}
