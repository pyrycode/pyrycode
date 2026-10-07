package streamsup

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modelFieldCapFixture records maxModelField as a LITERAL, deliberately not as
// the production constant. Same rule as taskStartedCapCheat: a fixture built from
// the constant it validates asserts nothing about the number — halve the constant
// and every row below would follow it green. This literal is what makes such an
// edit go RED.
//
// A FOURTH byte-cap fixture even though it equals taskFieldIDCapFixture and
// rateLimitFieldCapFixture, mirroring the production split: the three bound
// different fields for different reasons, and sharing a fixture would let a change
// to one silently retarget another's proof.
const modelFieldCapFixture = 256

// modelInitLineFixture builds a system/init line from the ONE mapped key. It
// invents NO field structure — `model` is the capture's own key — and exists only
// to vary the VALUE, which is what the cap proof needs and what the capture cannot
// supply: the captured value is 25 bytes and reaches no defensible cap.
//
// A model of "" emits `"model":""`. The ABSENT case is a different input and is
// written as a raw line at its call site rather than bent into this helper, for
// backgroundTaskRosterLineFixture's reason: both are asserted, so neither may be
// expressible only as the other.
func modelInitLineFixture(t *testing.T, model string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":    "system",
		"subtype": "init",
		"model":   model,
	})
	if err != nil {
		t.Fatalf("marshalling system/init fixture: %v", err)
	}
	return string(b)
}

// modelAnnouncedEvent drives one line through the shipped parser and returns the
// single ModelAnnounced it must emit.
func modelAnnouncedEvent(t *testing.T, line string) turnevent.ModelAnnounced {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.ModelAnnounced)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.ModelAnnounced", got[0])
	}
	return ev
}

// modelAnnouncedFromRichLine returns the ModelAnnounced produced by a line that
// also carries the keys #2252 maps, so the parser legitimately emits a
// turnevent.SessionFacts beside it.
//
// A SECOND READER rather than a loosening of modelAnnouncedEvent above, and the
// distinction is what keeps that helper's exactly-one rule worth having: every
// caller of it feeds modelInitLineFixture, which declares `model` alone, and there a
// second event WOULD be a defect. The captured lines are the ones carrying all three
// facts, and they are what this reader exists for.
func modelAnnouncedFromRichLine(t *testing.T, line string) turnevent.ModelAnnounced {
	t.Helper()
	got := collectEvents(line)
	var found []turnevent.ModelAnnounced
	for _, ev := range got {
		if ma, ok := ev.(turnevent.ModelAnnounced); ok {
			found = append(found, ma)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ModelAnnounced count: got %d, want 1 (all events: %#v)", len(found), got)
	}
	return found[0]
}

// TestParser_ModelAnnouncedMapsFromCapture is #1600's central assertion: the
// CAPTURED system/init line becomes one turnevent.ModelAnnounced carrying the
// model the capture shows, and NOTHING ELSE from the line.
//
// The Model assertion is DERIVED from the capture's own payload rather than
// pinned, which is this family's rule. The canary below is the one pinned literal,
// and unlike its siblings' canaries it pins a REAL value: the dropcap redactor
// rewrites session_id, cwd and the FIFO path, and it does not touch `model`.
//
// The whole-event reflect.DeepEqual is what carries "and nothing else from the
// line", and it is deliberately used INSTEAD of the family's per-key reflection
// sweep over string fields. The sweep exists so a field added to the variant later
// is covered without anyone extending a list; DeepEqual against a struct literal
// covers that strictly better — a later field that started carrying cwd, session_id
// or any of the other nineteen dropped keys is non-zero, and a literal leaving it
// zero fails. It also pins Truncated, which no string sweep would visit.
func TestParser_ModelAnnouncedMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "init")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	want, _ := payload["model"].(string)
	if want == "" {
		t.Fatalf("the capture carries no string \"model\", so the routing of Model cannot be proven against it")
	}
	// The two keys whose absence matters most (see turnevent.ModelAnnounced's doc),
	// checked to be PRESENT on the line so the DeepEqual below is a real statement
	// about dropping them rather than a statement about a line that never carried
	// them.
	for _, key := range []string{"cwd", "session_id"} {
		if v, _ := payload[key].(string); v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
	}

	// The rich reader: since #2252 this captured line also carries
	// claude_code_version and permissionMode, so it produces a SessionFacts beside the
	// announcement. What THIS test asserts is unchanged — one ModelAnnounced carrying
	// the model and nothing else from the line.
	ev := modelAnnouncedFromRichLine(t, string(line))

	if wantEv := (turnevent.ModelAnnounced{Model: want}); !reflect.DeepEqual(ev, wantEv) {
		t.Errorf("event: got %#v, want %#v — the model verbatim and nothing else from the line", ev, wantEv)
	}

	// The canary: proves the reader selected the init record rather than some other
	// system line that happens to decode into the same shape. It is also the
	// measurement turnevent.ModelAnnounced's doc rests on — claude DATED the bare
	// `haiku` alias this run was spawned with.
	if ev.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model: got %q, want the capture's one observed value %q", ev.Model, "claude-haiku-4-5-20251001")
	}
}

// TestParser_ModelAnnouncedCarriesTheValueVerbatim is the half of AC1 the capture
// CANNOT prove. Its model reads claude-haiku-4-5-20251001 — already lowercase,
// already dated, already fully formed — so a mapping that lowercased, expanded an
// alias, date-stamped, or rewrote against a model list would map it to itself and
// TestParser_ModelAnnouncedMapsFromCapture would stay green. These rows are
// synthesized for exactly that reason, and they invent no field structure: the only
// key is the capture's own `model`.
//
// The rule they pin is the MEASURED one (see the variant's doc): claude echoes an
// identifier at least as specific as the one it was given, so what arrives here is
// not reliably dated and need not appear in any published list. The daemon's job is
// to carry it, not to repair it.
func TestParser_ModelAnnouncedCarriesTheValueVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		model string
		why   string
	}{
		{
			name: "a bare family alias is NOT expanded", model: "haiku",
			why: "claude dates the alias on its own side; a daemon that expanded it would be inventing " +
				"the dated identifier rather than reporting one",
		},
		{
			name: "a value in no published model list survives", model: "claude-haiku-4-5",
			why: "MEASURED — the permission_protocol captures echo exactly this, and it appears in no " +
				"published list, so a lookup-and-replace mapping would drop a real announcement",
		},
		{
			name: "mixed case is NOT folded", model: "Claude-Sonnet-5-PREVIEW",
			why: "the one transform the capture's already-lowercase value could hide",
		},
		{
			name: "an unfamiliar naming scheme survives", model: "some-model-claude-has-not-shipped-yet",
			why: "the cap is the only judgement this mapping makes about the value's shape",
		},
		{
			// Re-homed VERBATIM from TestParser_IgnoredLineTypesStaySilent, where it
			// asserted the opposite. #1380 put it there deliberately, and it stayed
			// correct through four sibling tickets because the subtype was genuinely
			// still dropped; #1600 is what spends it.
			name:  "the row that used to assert this line was silent",
			model: "claude-opus-5",
			why:   "same line, opposite verdict — this is the change",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev := modelAnnouncedEvent(t, modelInitLineFixture(t, tt.model))
			if ev.Model != tt.model {
				t.Errorf("Model: got %q, want claude's value %q verbatim (%s)", ev.Model, tt.model, tt.why)
			}
			if ev.Truncated {
				t.Errorf("Truncated: got true, want false — every value here is far under the cap")
			}
		})
	}

	t.Run("the re-homed row's whole line, unedited", func(t *testing.T) {
		t.Parallel()
		// The row as TestParser_IgnoredLineTypesStaySilent carried it, session_id and
		// all: the fixture above rebuilds the line from one key, and this asserts the
		// verdict flipped for the LINE rather than for a reduced version of it.
		ev := modelAnnouncedEvent(t, `{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5"}`)
		if wantEv := (turnevent.ModelAnnounced{Model: "claude-opus-5"}); !reflect.DeepEqual(ev, wantEv) {
			t.Errorf("event: got %#v, want %#v", ev, wantEv)
		}
	})
}

// TestParser_ModelAnnouncedIsPerLine pins AC1's "per LINE". claude emits init once
// per TURN and the announcements within one session need not agree: #1582 measured
// three in one session — [claude-sonnet-5, claude-sonnet-5,
// claude-haiku-4-5-20251001] — because the /model turn emits its own init and that
// one still reports the OLD model.
//
// The three lines go through ONE parser, in order, because the failure this
// catches is a producer that latches or dedups: a first-wins implementation emits
// one event, a change-only one emits two, and only a per-line one emits three.
func TestParser_ModelAnnouncedIsPerLine(t *testing.T) {
	t.Parallel()
	// #1582's recorded sequence, verbatim — the repeat is the point, not an
	// oversight, and a dedup would swallow exactly it.
	models := []string{"claude-sonnet-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"}

	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	for _, m := range models {
		if _, err := p.Write([]byte(modelInitLineFixture(t, m) + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}

	if len(got) != len(models) {
		t.Fatalf("event count: got %d, want %d (one per line) — %#v", len(got), len(models), got)
	}
	for i, ev := range got {
		ma, ok := ev.(turnevent.ModelAnnounced)
		if !ok {
			t.Fatalf("event %d: got %T, want turnevent.ModelAnnounced", i, ev)
		}
		if ma.Model != models[i] {
			t.Errorf("event %d Model: got %q, want %q — the events must follow the lines in order", i, ma.Model, models[i])
		}
	}
}

// TestParser_ModelAnnouncedFieldCap pins the construction-time bound. It is
// applied before the event reaches the sink, so an oversized value never enters
// the event stream, a queue, or a log — the same ordering maxUnrecognizedRaw's cap
// has, and the only thing that makes the bound real rather than cosmetic.
//
// The capture proves the mapping and CANNOT prove the bound: its model is 25
// bytes, and so is every other model on record (16 and 15 bytes), so no observed
// value reaches any defensible cap. These lines are therefore necessarily
// synthesized, and they invent no field structure — the key is the capture's, only
// the value is oversized.
//
// The exact-cut length is asserted on ASCII fixtures only. The scrub uses an empty
// replacement (truncateRaw's precedent), so a mid-rune cut DELETES the partial rune
// and a multi-byte result is legitimately 1-3 bytes short of the cap; that row
// asserts validity and the ceiling instead.
func TestParser_ModelAnnouncedFieldCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		model         string
		wantLen       int
		wantTruncated bool
		why           string
	}{
		{
			name: "a realistic value is untouched", model: "claude-haiku-4-5-20251001",
			wantLen: len("claude-haiku-4-5-20251001"), wantTruncated: false,
			why: "the observed maximum, ~10x under the cap",
		},
		{
			name: "exactly at the cap is NOT truncated", model: strings.Repeat("m", modelFieldCapFixture),
			wantLen: modelFieldCapFixture, wantTruncated: false,
			why: "truncateField's boundary is <=, and a boundary asserted nowhere is a boundary that drifts",
		},
		{
			name: "one byte over the cap is cut and reported", model: strings.Repeat("m", modelFieldCapFixture+1),
			wantLen: modelFieldCapFixture, wantTruncated: true,
			why: "the smallest input that must cut",
		},
		{
			name: "far over the cap is cut to the same length", model: strings.Repeat("m", modelFieldCapFixture*4),
			wantLen: modelFieldCapFixture, wantTruncated: true,
			why: "the cut is to the cap, not proportional to the input",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev := modelAnnouncedEvent(t, modelInitLineFixture(t, tt.model))
			if len(ev.Model) != tt.wantLen {
				t.Errorf("len(Model): got %d, want %d (%s)", len(ev.Model), tt.wantLen, tt.why)
			}
			if ev.Truncated != tt.wantTruncated {
				t.Errorf("Truncated: got %v, want %v (%s)", ev.Truncated, tt.wantTruncated, tt.why)
			}
		})
	}

	t.Run("a cut landing mid-rune leaves valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// 255 ASCII bytes then one 3-byte rune: the cut at the cap lands INSIDE the
		// rune, and the empty replacement deletes the partial rune rather than
		// replacing it.
		ev := modelAnnouncedEvent(t, modelInitLineFixture(t, strings.Repeat("m", modelFieldCapFixture-1)+"€"))
		if !ev.Truncated {
			t.Fatalf("Truncated: got false, want true")
		}
		if !utf8.ValidString(ev.Model) {
			t.Errorf("Model is not valid UTF-8 after a mid-rune cut: %q", ev.Model)
		}
		// Short of the cap, not at it — and not more than a rune short.
		if len(ev.Model) > modelFieldCapFixture || len(ev.Model) < modelFieldCapFixture-3 {
			t.Errorf("len(Model): got %d, want within 3 bytes below %d", len(ev.Model), modelFieldCapFixture)
		}
	})
}

// TestParser_ModelAnnouncedDropsAreConsumedAndSilent is AC3. Three inputs produce
// no event, and the DIFFERENCE between them is what this pins: the undecodable one
// logs exactly one content-free record, and the two model-less ones log NOTHING AT
// ALL.
//
// The silence is a decision, not an omission. emitRateLimit logs a reason keyword
// on each of its rungs because it fires once per RUN and has three distinguishable
// ones; init fires once per TURN and has a single non-undecodable drop reason, so a
// Debug there would put a record in the daemon log on every turn — reinstating in
// the log exactly the per-turn noise row TestParser_IgnoredLineTypesStaySilent's
// doc exists to prevent in the event stream. emitThinkingProgress's delta <= 0 arm
// is the precedent.
//
// The undecodable row's attrs are asserted EXACTLY rather than swept for the value,
// and that is the load-bearing difference. encoding/json QUOTES the offending input
// into its error text rather than reproducing it verbatim, so "err", err can leak a
// value a strings.Contains sweep does not recognise; only "these attrs and no
// others" catches it. The row's model is a long digit run so the sweep below has
// something distinctive to look for either way.
func TestParser_ModelAnnouncedDropsAreConsumedAndSilent(t *testing.T) {
	t.Parallel()
	const undecodableDigits = "160016001600160016001600"
	tests := []struct {
		name      string
		line      string
		wantAttrs map[string]string
		why       string
	}{
		{
			name: "model absent", line: `{"type":"system","subtype":"init","session_id":"s"}`,
			wantAttrs: nil,
			why: "absence is claude's to choose, and an event carrying an empty model would assert " +
				"`claude announced a model` while naming none — a claim the line did not make",
		},
		{
			name: "model present but empty", line: modelInitLineFixture(t, ""),
			wantAttrs: nil,
			why:       "answered identically to absence, which is what makes a plain-string decode target sufficient",
		},
		{
			name: "model is not a string", line: `{"type":"system","subtype":"init","model":` + undecodableDigits + `}`,
			wantAttrs: map[string]string{"subtype": "init"},
			why: "a non-string model fails the WHOLE decode; the subtype is a message-name keyword, " +
				"not payload, and it is the only thing this record may carry",
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
				// An Unrecognized here would be its own failure: keeping `system` whole on
				// ignoredLineTypes is what makes "no system line reaches the unrecognized
				// lane" structural, and that outranks surfacing a malformed line of a
				// subtype we already know.
				t.Errorf("event count: got %d, want 0 (%s) — %#v", len(events), tt.why, events)
			}
			// The line is CONSUMED on every path: emitModelAnnounced returns true, so it
			// never falls through to consumeLine's generic drop.
			if generic := rec.withMessage(genericDropMsgFixture); len(generic) != 0 {
				t.Errorf("the line reached consumeLine's generic drop (%d record(s)): %+v", len(generic), generic)
			}

			all := rec.all()
			if tt.wantAttrs == nil {
				if len(all) != 0 {
					t.Errorf("records: got %d, want 0 — this drop is silent by design, and a per-turn "+
						"Debug is the noise this arm exists to avoid: %+v", len(all), all)
				}
				return
			}
			drops := rec.withMessage(undecodableSystemLineMsgFixture)
			if len(drops) != 1 {
				t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)",
					undecodableSystemLineMsgFixture, len(drops), all)
			}
			if !reflect.DeepEqual(drops[0].attrs, tt.wantAttrs) {
				t.Errorf("drop attrs: got %v, want exactly %v (%s) — an `err` attr here would carry "+
					"encoding/json's quoted copy of the input", drops[0].attrs, tt.wantAttrs, tt.why)
			}
			for _, r := range all {
				if strings.Contains(r.msg, undecodableDigits) {
					t.Errorf("record message carries the offending value: %q", r.msg)
				}
				for k, v := range r.attrs {
					if strings.Contains(v, undecodableDigits) {
						t.Errorf("record %q attr %q carries the offending value", r.msg, k)
					}
				}
			}
		})
	}

	// The control: without a line that must NOT be silent, every assertion above
	// passes against an arm that does nothing at all.
	t.Run("control_a_model_carrying_line_emits", func(t *testing.T) {
		t.Parallel()
		if got := collectEvents(modelInitLineFixture(t, "claude-haiku-4-5")); len(got) != 1 {
			t.Fatalf("control: a model-carrying init emitted %d events, want 1 — the silence above proves nothing", len(got))
		}
	})
}

// TestParser_ModelAnnouncedIsLoggedContentFree is the assertion the per-path attr
// checks do NOT cover: an implementation that gets every attr right AND also logs
// "model", il.Model on the emit path passes every one of them.
//
// It sweeps every record's message and every attribute value on the EMIT path as
// well as both drop paths, which is the half a per-path check cannot see — the emit
// path must log NOTHING, and there is no expected record there to assert against.
// Mirrors TestParser_RateLimitIsLoggedContentFree.
//
// The sweep covers the capture's cwd and session_id as well as the model. Those two
// are not fields on the decode target at all, so the sweep proves the omission
// holds END TO END rather than only at the struct — and cwd is the operator's local
// filesystem path, which is the most revealing thing this line carries.
//
// The constraint is #833's posture, restated across internal/relay's
// v2session_settings.go and internal/sessions' pool.go as "model / effort / YOLO
// values are NEVER logged at any level". It is scoped to LOGS and says nothing
// about the event stream, which is why an emitSystemSubtype arm does not violate
// it.
func TestParser_ModelAnnouncedIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	const (
		emitModel        = "model-sentinel-160021"
		undecodableModel = "160023160023160023"
	)
	captured := capturedSystemLine(t, "init")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedModel, _ := payload["model"].(string)
	capturedCwd, _ := payload["cwd"].(string)
	capturedSession, _ := payload["session_id"].(string)
	if capturedModel == "" || capturedCwd == "" || capturedSession == "" {
		t.Fatalf("the capture carries no string model/cwd/session_id, so this sweep would be vacuous")
	}

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	lines := []string{
		// The emit path, first and twice: the synthesized sentinel, then the real
		// captured line with its cwd and session_id intact.
		modelInitLineFixture(t, emitModel),
		string(captured),
		// Both drop paths.
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"system","subtype":"init","model":` + undecodableModel + `}`,
	}
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}

	// Three, not two, since #2252: the two model-carrying lines each produce a
	// ModelAnnounced, and the CAPTURED one carries claude_code_version and
	// permissionMode as well, so it produces a SessionFacts too. What this test asserts
	// is unchanged — it is about the log, and the count is here so a producer that
	// silently stopped emitting cannot pass the sweep by logging nothing.
	if len(events) != 3 {
		t.Fatalf("event count: got %d, want 3 (two model-carrying lines, one of them also "+
			"carrying the #2252 facts) — %#v", len(events), events)
	}
	// ONE record and not two, three or four: the emit path logs nothing, the
	// model-less line logs nothing, and only the undecodable line speaks. That count
	// is the half of this test the per-path assertions cannot see.
	if all := rec.all(); len(all) != 1 {
		t.Errorf("records: got %d, want 1 (the undecodable line only): %+v", len(all), all)
	}

	leaks := []string{emitModel, undecodableModel, capturedModel, capturedCwd, capturedSession}
	for _, r := range rec.all() {
		for _, leak := range leaks {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content (%q): %q", leak, r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content (%q); this path logs a "+
						"daemon-authored subtype keyword only", r.msg, k, leak)
				}
			}
		}
	}
}

// --- #2134: conversation_reset -------------------------------------------------

// conversationResetIDFixture is the canonical stem the #2134 rows announce. It is
// a RECONSTRUCTED value, not a captured one, and the distinction is recorded
// because it is weaker than what the ModelAnnounced rows rest on: the parent
// ticket's capture (claude 2.1.259, observed 2026-09-04) elides both id values
// (`c43fbe8d-…`, `a2a0b27a-…`), and a tree-wide grep finds no capture file holding
// a conversation_reset line. So the SHAPE below is claude's and the VALUE is ours.
// Live confirmation is #2138's job.
//
// The digits are chosen to be conspicuous — a descending nibble run no log
// message, message name or sibling fixture in this package contains — so a
// contains-sweep for a leak cannot pass by colliding with unrelated text.
const conversationResetIDFixture = "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0"

// unrecognizedPayloadMsgFixture is emitUnrecognized's Debug message, as a literal
// for harnessNudgeDropMsg's reason. #2134's decline rows select on it: the arm
// adds NO record of its own, so this is the only one a declined reset may produce.
const unrecognizedPayloadMsgFixture = "streamsup: unrecognized payload"

// conversationResetLineFixture builds a conversation_reset line from the two keys
// the parent ticket's capture shows, inventing no field structure. `uuid` rides
// along precisely because nothing reads it: carrying it proves the decode ignores
// an undeclared sibling key rather than merely never meeting one.
//
// The ABSENT-key case is a different input and is written as a raw line at its
// call site rather than bent into this helper, for modelInitLineFixture's reason:
// both are asserted, so neither may be expressible only as the other.
func conversationResetLineFixture(t *testing.T, newID string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":                "conversation_reset",
		"new_conversation_id": newID,
		"uuid":                "a2a0b27a-1111-4222-8333-444455556666",
	})
	if err != nil {
		t.Fatalf("marshalling conversation_reset fixture: %v", err)
	}
	return string(b)
}

// TestParser_ConversationResetMapsFromTheAnnouncement is AC1. A canonical
// new_conversation_id becomes exactly ONE turnevent.ConversationReset carrying it,
// and the line produces NO Unrecognized — the noise row this ticket removes.
//
// Both halves are asserted, and over the WHOLE slice rather than element 0.
// "exactly one event and it is the right one" and "no noise row" are two claims,
// and AC1 makes both: a future arm that emitted the reset AND fell through would
// satisfy a first-element check while re-introducing the defect.
func TestParser_ConversationResetMapsFromTheAnnouncement(t *testing.T) {
	t.Parallel()

	got := collectEvents(conversationResetLineFixture(t, conversationResetIDFixture))

	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 — %#v", len(got), got)
	}
	want := turnevent.ConversationReset{NewConversationID: conversationResetIDFixture}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("event: got %#v, want %#v", got[0], want)
	}
	for i, ev := range got {
		if un, ok := ev.(turnevent.Unrecognized); ok {
			t.Errorf("event %d is an Unrecognized (%+v); removing that row for this type is the ticket", i, un)
		}
	}
}

// TestParser_ConversationResetCarriesTheIDVerbatim is the half of AC1 the single
// fixture above cannot prove. A producer that lowercased, trimmed, re-hyphenated
// or otherwise re-formatted the id would keep that test green, because its one
// value is already in the form such a transform produces. These rows vary the
// value inside the canonical set so any normalisation reddens.
func TestParser_ConversationResetCarriesTheIDVerbatim(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		conversationResetIDFixture,
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"deadbeef-cafe-4bad-8f00-0123456789ab",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(conversationResetLineFixture(t, id))
			if len(got) != 1 {
				t.Fatalf("event count: got %d, want 1 — %#v", len(got), got)
			}
			ev, ok := got[0].(turnevent.ConversationReset)
			if !ok {
				t.Fatalf("event type: got %T, want turnevent.ConversationReset", got[0])
			}
			if ev.NewConversationID != id {
				t.Errorf("NewConversationID: got %q, want %q byte-for-byte", ev.NewConversationID, id)
			}
		})
	}
}

// TestParser_ConversationResetDeclinesReachTheUnrecognizedLane is AC2. A reset the
// daemon cannot act on produces NO reset event and reaches the unrecognized lane
// instead — deliberately declining the guarantee rate_limit_event and
// control_response each hold ("emitUnrecognized unreachable BY MATCHING"), because
// an announcement the parser silently swallows is the defect being fixed.
//
// THE ROWS SPLIT INTO TWO GROUPS AND THE SPLIT IS ASSERTED, not just described.
// byPredicate rows must be well-formed JSON strings so the DECODE SUCCEEDS and
// transcript.ValidStem is what rejects them; the loop re-decodes each one and
// requires the hostile value to come back intact, because a row written carelessly
// as a malformed payload would fail at json.Unmarshal instead, land on the very
// same decline path, pass green, and prove nothing about the gate. The numeric row
// is the ONE that is supposed to fail the decode.
//
// The traversal- and injection-shaped rows are the security review's, and the
// newline one is the sharpest: some regexp dialects let `$` match before a
// trailing newline, which would hand a downstream <dir>/<id>.jsonl resolver a path
// with an embedded line break. Go's `$` is \z semantics, so it does not — pinned
// here rather than trusted.
func TestParser_ConversationResetDeclinesReachTheUnrecognizedLane(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		line        string
		byPredicate string // the value the decode must yield before the gate rejects it
		why         string
	}{
		{
			name: "key absent", line: `{"type":"conversation_reset","uuid":"a2a0b27a-1111-4222-8333-444455556666"}`,
			why: "absent, present-but-empty and a line carrying no such key are answered identically, " +
				"which is what makes a plain-string decode target sufficient",
		},
		{
			name: "present but empty", line: conversationResetLineFixture(t, ""), byPredicate: "",
			why: "an event carrying an empty id would assert `claude mounted a new transcript` while naming none",
		},
		{
			name: "uppercase hex", line: conversationResetLineFixture(t, "0F1E2D3C-4B5A-4978-8796-A5B4C3D2E1F0"),
			byPredicate: "0F1E2D3C-4B5A-4978-8796-A5B4C3D2E1F0",
			why:         "ValidStem is lowercase-only; repairing the case here would be inventing rather than reporting",
		},
		{
			name: "one char short", line: conversationResetLineFixture(t, "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f"),
			byPredicate: "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f",
			why:         "the pattern is a fixed-length full match, so a near-miss is a miss",
		},
		{
			name: "one char long", line: conversationResetLineFixture(t, conversationResetIDFixture+"0"),
			byPredicate: conversationResetIDFixture + "0",
			why:         "anchored at both ends: a canonical stem with anything appended is not one",
		},
		{
			name: "hyphens misplaced", line: conversationResetLineFixture(t, "0f1e2d3c4-b5a-4978-8796-a5b4c3d2e1f0"),
			byPredicate: "0f1e2d3c4-b5a-4978-8796-a5b4c3d2e1f0",
			why:         "group lengths are part of the shape, not decoration",
		},
		{
			name: "non-hex character", line: conversationResetLineFixture(t, "0g1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0"),
			byPredicate: "0g1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0",
			why:         "the alphabet is [0-9a-f] and a hyphen, and nothing else",
		},
		{
			name: "path traversal", line: conversationResetLineFixture(t, "../../etc/passwd"),
			byPredicate: "../../etc/passwd",
			why: "SECURITY: downstream resolves <dir>/<id>.jsonl, and the gate's alphabet excludes " +
				"the separator and the dot by construction",
		},
		{
			name: "stem with a separator", line: conversationResetLineFixture(t, "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e/f0"),
			byPredicate: "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e/f0",
			why:         "SECURITY: a right-length, right-looking value carrying one separator is the near-miss that matters",
		},
		{
			name: "stem with a dot", line: conversationResetLineFixture(t, "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e.f0"),
			byPredicate: "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e.f0",
			why:         "SECURITY: the dot is the other half of a traversal and is excluded the same way",
		},
		{
			name: "trailing newline", line: conversationResetLineFixture(t, conversationResetIDFixture+"\n"),
			byPredicate: conversationResetIDFixture + "\n",
			why: "SECURITY: Go's `$` is \\z, not Perl's before-final-newline — a canonical stem with a " +
				"newline glued on must NOT pass, or a resolver downstream receives a path with a line break",
		},
		{
			name: "newline then a path", line: conversationResetLineFixture(t, conversationResetIDFixture+"\n../../etc/passwd"),
			byPredicate: conversationResetIDFixture + "\n../../etc/passwd",
			why:         "SECURITY: the trailing-newline row's exploit form, pinned beside it",
		},
		{
			name: "id is not a string", line: `{"type":"conversation_reset","new_conversation_id":21342134}`,
			why: "the ONE row that fails the whole decode rather than the gate; a numeric id is not a " +
				"canonical stem either, so it takes the same decline path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Non-vacuity: prove the DECODE succeeds on the rows that are meant to be
			// rejected by the predicate, so a fixture that quietly became malformed
			// cannot pass this test for the wrong reason.
			if tt.byPredicate != "" || tt.name == "present but empty" {
				var probe struct {
					ID string `json:"new_conversation_id"`
				}
				if err := json.Unmarshal([]byte(tt.line), &probe); err != nil {
					t.Fatalf("row is meant to be rejected by ValidStem, but its payload does not decode: %v", err)
				}
				if probe.ID != tt.byPredicate {
					t.Fatalf("row's decoded id: got %q, want %q — the fixture no longer carries the hostile value",
						probe.ID, tt.byPredicate)
				}
			}

			got := collectEvents(tt.line)

			for i, ev := range got {
				if cr, ok := ev.(turnevent.ConversationReset); ok {
					t.Fatalf("event %d is a ConversationReset carrying %q, want none (%s)", i, cr.NewConversationID, tt.why)
				}
			}
			if len(got) != 1 {
				t.Fatalf("event count: got %d, want 1 Unrecognized (%s) — %#v", len(got), tt.why, got)
			}
			un, ok := got[0].(turnevent.Unrecognized)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.Unrecognized (%s)", got[0], tt.why)
			}
			if un.Site != turnevent.UnrecognizedLineType {
				t.Errorf("Unrecognized.Site: got %q, want %q", un.Site, turnevent.UnrecognizedLineType)
			}
			if un.Kind != "conversation_reset" {
				t.Errorf("Unrecognized.Kind: got %q, want %q", un.Kind, "conversation_reset")
			}
		})
	}
}

// TestParser_ConversationResetDeclineAddsNoDropRecord pins the logging half of
// AC2, and the claim is narrower than "the decline is silent": the line DOES
// produce exactly one record, emitUnrecognized's own content-free one. What the
// arm must not add is a SECOND, weaker record of the same fact.
//
// That DIVERGES from every sibling arm and the divergence is the decision being
// pinned. emitRateLimit logs a reason keyword per rung and emitModelAnnounced logs
// its undecodable drop because those arms CONSUME their declines, so a Debug is
// the only trace they can leave. Here the decline is SURFACED to a client as an
// Unrecognized carrying the offending bytes — strictly more visible than a Debug
// the production daemon does not print — so a drop-reason vocabulary would be
// redundant by construction.
//
// The attrs are then swept for the id. emitUnrecognized reports `bytes` and
// `truncated`, never the raw line, and the EVENT is where the bytes travel; this
// is the one place a future edit would most plausibly break that.
func TestParser_ConversationResetDeclineAddsNoDropRecord(t *testing.T) {
	t.Parallel()

	const hostile = "../../etc/" + conversationResetIDFixture
	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write([]byte(conversationResetLineFixture(t, hostile) + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	all := rec.all()
	if len(all) != 1 {
		t.Fatalf("records: got %d, want exactly 1 (emitUnrecognized's) — the arm adds none of its own: %+v",
			len(all), all)
	}
	if all[0].msg != unrecognizedPayloadMsgFixture {
		t.Errorf("record message: got %q, want %q", all[0].msg, unrecognizedPayloadMsgFixture)
	}
	if generic := rec.withMessage(genericDropMsgFixture); len(generic) != 0 {
		t.Errorf("the line reached consumeLine's generic drop (%d record(s)): %+v", len(generic), generic)
	}
	for key, value := range all[0].attrs {
		if strings.Contains(value, hostile) || strings.Contains(value, conversationResetIDFixture) {
			t.Errorf("claude's id leaked into log attr %q = %q", key, value)
		}
	}
}

// TestParser_ConversationResetNeighbourTypeStillRingsTheBell is AC3, scoped to the
// regression the new arm makes possible. The general property — an unmapped,
// unlisted top-level type reaches the unrecognized lane — is already covered; what
// is new is that a case label one edit away from `conversation_reset` must not be
// widened into it. A prefix, a suffix and the plural each stay unrecognized.
func TestParser_ConversationResetNeighbourTypeStillRingsTheBell(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{"conversation_resets", "conversation_reset_v2", "pre_conversation_reset", "conversation"} {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			line := `{"type":"` + typ + `","new_conversation_id":"` + conversationResetIDFixture + `"}`
			got := collectEvents(line)
			if len(got) != 1 {
				t.Fatalf("event count: got %d, want 1 Unrecognized — %#v", len(got), got)
			}
			un, ok := got[0].(turnevent.Unrecognized)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.Unrecognized — the arm matched a neighbouring type", got[0])
			}
			if un.Kind != typ {
				t.Errorf("Unrecognized.Kind: got %q, want %q", un.Kind, typ)
			}
		})
	}
}
