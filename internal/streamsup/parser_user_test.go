package streamsup

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// harnessNudgeFixture is claude's no-visible-output self-nudge as a string
// LITERAL, transcribed byte-exact from the #1247 capture (claude 2.1.220, the
// #1240 probe, 3 of 3).
//
// Deliberately NOT harnessNoOutputNudge: a fixture built from the constant it
// validates asserts nothing about the bytes — edit the constant and every test
// here follows it green. This literal is what makes such an edit go RED, which
// is the point of the suppression's whole test set. Every fixture below builds
// on this literal, never on the production constant.
const harnessNudgeFixture = "[Your previous response had no visible output. Please continue and produce a user-visible response.]"

// harnessNudgeDropMsg is the Debug message the drop site emits, as a literal for
// the same reason as harnessNudgeFixture.
const harnessNudgeDropMsg = "streamsup: dropping known harness user block"

// TestParser_HarnessNudgeDropIsLoggedContentFree is AC4 of #1247: the drop is
// Debug-logged with the site and block type ONLY, never the text — the package's
// standing content-free logging rule, matching the existing known-ignored drop.
//
// The third assertion is the load-bearing one. The first two only describe what
// is present; "no captured attr value contains the nudge" is what catches the
// realistic way this rule gets broken later, someone appending "text",
// block.Text to the log line. The payload itself is harmless boilerplate — the
// rule is about the SITE, which would carry an arbitrary user/text block's
// content the day the guard is widened.
func TestParser_HarnessNudgeDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))

	line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + harnessNudgeFixture + `"}]}}`
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	drops := rec.withMessage(harnessNudgeDropMsg)
	if len(drops) != 1 {
		t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)", harnessNudgeDropMsg, len(drops), rec.all())
	}
	wantAttrs := map[string]string{
		"site": string(turnevent.UnrecognizedUserBlock),
		"type": "text",
	}
	if !reflect.DeepEqual(drops[0].attrs, wantAttrs) {
		t.Errorf("drop attrs: got %v, want exactly %v", drops[0].attrs, wantAttrs)
	}
	for _, r := range rec.all() {
		for k, v := range r.attrs {
			if strings.Contains(v, harnessNudgeFixture) {
				t.Errorf("record %q attr %q carries the block text; the drop site logs site and type only", r.msg, k)
			}
		}
		if strings.Contains(r.msg, harnessNudgeFixture) {
			t.Errorf("record message carries the block text: %q", r.msg)
		}
	}
}

// syntheticFlagFixture is the top-level key/value claude stamps on a
// harness-authored user line, as a LITERAL for harnessNudgeFixture's reason: a
// fixture built from the struct tag it validates follows a retag green.
//
// Two dead spellings are deliberately NOT this string, and both are one plausible
// generalisation away. `isMeta` is what the JSONL TRANSCRIPT calls the same class
// of line (415 occurrences in the operator's corpus on 2026-09-06, against zero
// truthy isSynthetic) — this package does not read the transcript. `is_synthetic`
// is what a reader gets by over-applying #2023's finding that the sidecar envelope
// key is snake_case here; the envelope is MIXED, not uniformly snake_case, and
// the captured line proves it (`parent_tool_use_id`, `session_id` … `isSynthetic`).
// Either spelling compiles, decodes nothing, and never fires.
const syntheticFlagFixture = `"isSynthetic":true`

// syntheticSkillProbe stands in for a skill body: distinctive enough that the
// content-free sweep below cannot match it by accident, and pointedly NOT the
// nudge, so a case that drops it can only have dropped it via the flag.
const syntheticSkillProbe = "pyry-2087 skill body probe: Base directory for this skill: /tmp/x"

// syntheticUserLine builds a user line carrying blocks, with the synthetic flag
// as a raw fragment so a case can supply `"isSynthetic":true`, the false form, a
// misspelling, or nothing at all.
func syntheticUserLine(flag, blocks string) string {
	line := `{"type":"user","message":{"role":"user","content":[` + blocks + `]}`
	if flag != "" {
		line += "," + flag
	}
	return line + "}"
}

// textBlock and toolResultBlock keep the table's rows to the bytes under test.
func textBlock(text string) string {
	return `{"type":"text","text":"` + text + `"}`
}

func toolResultBlock(id string) string {
	return `{"type":"tool_result","tool_use_id":"` + id + `","content":"x","is_error":false}`
}

// TestParser_SyntheticUserLineDropsTextBlocks is #2087 AC1: the behaviour matrix
// of the flag arm.
//
// The arm exists because invoking a skill puts the skill's whole body on the
// stream as a user/text block — 87244 chars in one observed case — and every one
// of them became an "Unrecognized message" row in the operator's chat. Matching
// the BODY could never work: it varies per skill and dwarfs any pin. Matching the
// line-level flag is what keeps a genuinely new user/text block reaching the
// unrecognized lane while the harness's own prose does not.
//
// The rows are chosen so that each one fails alone under a specific wrong
// implementation. Widen the arm past `text` and the unknown-type row reddens;
// scope it to the message rather than the block and the sibling-tool_result row
// reddens; key it on the KEY's presence rather than its value and the explicit
// -false row reddens; let it subsume the nudge constant and the unflagged-nudge
// row in TestParser_LineMapping reddens.
func TestParser_SyntheticUserLineDropsTextBlocks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want []turnevent.Event
	}{
		{
			// THE TICKET. A flagged text block emits zero events — not an
			// Unrecognized, not a TextChunk. Nothing reaches the client at all.
			name: "flagged text block is dropped in silence",
			line: syntheticUserLine(syntheticFlagFixture, textBlock(syntheticSkillProbe)),
			want: nil,
		},
		{
			// The suppression is scoped to the BLOCK. A message-level guard or an
			// early return would also emit no Unrecognized for the text, and only a
			// sibling that still maps tells the two apart.
			name: "flagged line's tool_result still maps",
			line: syntheticUserLine(syntheticFlagFixture,
				textBlock(syntheticSkillProbe)+","+toolResultBlock("tu-2087a")),
			want: []turnevent.Event{turnevent.ToolUpdate{
				ToolCallID: "tu-2087a",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: "x"},
			}},
		},
		{
			// …with its sidecar detail intact. The flag must not disturb the
			// tool_use_result decode that shares the line, which is the other
			// top-level field emitUser reads.
			name: "flagged line's tool_result keeps its sidecar detail",
			line: `{"type":"user","message":{"role":"user","content":[` +
				toolResultBlock("tu-2087b") + `]},"tool_use_result":` +
				editSidecar(`[{"lines":["+a"]}]`) + `,` + syntheticFlagFixture + `}`,
			want: []turnevent.Event{turnevent.ToolUpdate{
				ToolCallID:   "tu-2087b",
				Status:       turnevent.ToolStatusCompleted,
				Content:      turnevent.TextContent{Text: "x"},
				ResultDetail: "+1",
			}},
		},
		{
			// THE TRUST BOUNDARY of this change, and the reason the arm is gated on
			// `text` rather than on "everything but tool_result". A genuinely new
			// block type is the alarm the whole feature exists to raise; the flag
			// must not blanket it. Hostile tool output cannot reach the text arm
			// either — a tool_result's payload decodes into Content, never Text —
			// so tripping the guard needs control of the block's TYPE.
			name: "flagged line's unknown block type still surfaces",
			line: syntheticUserLine(syntheticFlagFixture, `{"type":"image","text":"ignored"}`),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "image",
				Raw:  `{"type":"image","text":"ignored"}`,
			}},
		},
		{
			// Read by VALUE, not by presence. A presence-only decode (the jsonKey
			// shape used elsewhere in this file) would swallow this, and claude
			// stamping the flag false on a line it wants seen is exactly the case a
			// presence check gets backwards.
			name: "explicit isSynthetic false still surfaces the text",
			line: syntheticUserLine(`"isSynthetic":false`, textBlock("plain user text")),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  `{"type":"text","text":"plain user text"}`,
			}},
		},
		{
			// The transcript's spelling, which this surface does not use. If the
			// decoder is ever "corrected" to isMeta it fires on nothing real, and
			// this row is what says so — the failure #2023 bought once already, from
			// the opposite direction.
			name: "the transcript spelling isMeta does not suppress",
			line: syntheticUserLine(`"isMeta":true`, textBlock(syntheticSkillProbe)),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  textBlock(syntheticSkillProbe),
			}},
		},
		{
			// The snake_case spelling a reader gets by over-applying #2023.
			name: "the snake_case spelling is_synthetic does not suppress",
			line: syntheticUserLine(`"is_synthetic":true`, textBlock(syntheticSkillProbe)),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  textBlock(syntheticSkillProbe),
			}},
		},
		{
			// AC3: the nudge constant survives as an INDEPENDENT guard. A claude
			// version that stops stamping the flag must not resurrect the nudge row,
			// so the two triggers are OR'd rather than one subsuming the other.
			name: "unflagged nudge is still dropped by its own constant",
			line: syntheticUserLine("", textBlock(harnessNudgeFixture)),
			want: nil,
		},
		{
			// The converse of the row above, and the pair is what proves the arms
			// are independent: unflagged non-nudge text still surfaces, so the flag
			// arm did not quietly widen into "drop all user text".
			name: "unflagged non-nudge text still surfaces",
			line: syntheticUserLine("", textBlock(syntheticSkillProbe)),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  textBlock(syntheticSkillProbe),
			}},
		},
		{
			// Scope is emitUser. The same flag on an ASSISTANT line is model speech
			// and still maps — silencing it there would drop what the person came to
			// read. Mirrors the nudge's own assistant-surface row.
			name: "the flag on an assistant line does not suppress model speech",
			line: `{"type":"assistant","message":{"id":"msg-2087","role":"assistant","content":[` +
				textBlock("model words") + `]},` + syntheticFlagFixture + `}`,
			want: []turnevent.Event{turnevent.TextChunk{MessageID: "msg-2087", Text: "model words"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("events for %s:\ngot  %#v\nwant %#v", tc.name, got, tc.want)
			}
		})
	}
}

// interruptNoticeShortFixture and interruptNoticeToolUseFixture are the TWO
// wordings claude 2.1.220 produced for the same event, on the same branch, on the
// same day (2026-08-19) — the dispatcher gate run got the tool-use form, a local
// verification run got the short one. They are LITERALS for harnessNudgeFixture's
// reason, and here the rule bites harder than it does there: the production
// matcher is a PREFIX, so a fixture built from the constant would be a prefix of
// itself and every row below would pass under any wording whatsoever.
//
// Between them they are also the argument for the prefix. Neither is a substring
// of the other past the shared head, so an exact-literal pin on either goes red on
// the other — which is what #1500's test-side carve-out found by going red first.
const (
	interruptNoticeShortFixture   = "[Request interrupted by user]"
	interruptNoticeToolUseFixture = "[Request interrupted by user for tool use]"
)

// TestParser_InterruptNoticeIsDropped is #1611 AC1 and AC2: the behaviour matrix
// of the interrupt-notice arm.
//
// The arm exists because interrupting a turn makes claude's harness inject a
// user/text block narrating its own cancellation, and it trips neither existing
// trigger: it is not the nudge, and 31 of 31 occurrences in the operator's
// transcript corpus carry no harness flag at all (measured 2026-09-14 over 6684
// files). So every interrupt — a routine action — put an "Unrecognized message"
// row in the operator's history, and the client already had the cancelled
// turn_end for the same event.
//
// The rows are chosen so each one fails alone under a specific wrong
// implementation. Pin an exact literal and one wording row reddens; return
// instead of continue and the sibling-tool_result row reddens; TrimSpace and the
// leading-space row reddens; Contains and the mid-string row reddens; widen past
// `text` and the unknown-type row reddens; put the guard anywhere but emitUser and
// the assistant row reddens.
func TestParser_InterruptNoticeIsDropped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want []turnevent.Event
	}{
		{
			// THE TICKET, wording 1 of 2. Zero events — not an Unrecognized, not a
			// TextChunk. Nothing reaches the client at all.
			name: "short wording is dropped in silence",
			line: syntheticUserLine("", textBlock(interruptNoticeShortFixture)),
			want: nil,
		},
		{
			// Wording 2 of 2, and the reason the match is a prefix rather than a
			// literal. One claude version produced both; a pin on either sibling
			// would leave this row red.
			name: "tool-use wording is dropped by the same matcher",
			line: syntheticUserLine("", textBlock(interruptNoticeToolUseFixture)),
			want: nil,
		},
		{
			// The suppression is scoped to the BLOCK (AC1's `continue`, not
			// `return`). A message-level guard or an early return also emits no
			// event for the text, and only a sibling that still maps tells them
			// apart.
			name: "the notice's sibling tool_result still maps",
			line: syntheticUserLine("",
				textBlock(interruptNoticeShortFixture)+","+toolResultBlock("tu-1611a")),
			want: []turnevent.Event{turnevent.ToolUpdate{
				ToolCallID: "tu-1611a",
				Status:     turnevent.ToolStatusCompleted,
				Content:    turnevent.TextContent{Text: "x"},
			}},
		},
		{
			// AC2: an unflagged text block sharing no prefix still reaches the
			// unrecognized lane. The converse of row 1, and the pair is what says
			// the arm did not quietly widen into "drop all user text".
			name: "unflagged text sharing no prefix still surfaces",
			line: syntheticUserLine("", textBlock("please stop doing that")),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  textBlock("please stop doing that"),
			}},
		},
		{
			// NO TRIM. #1243 matched this same prefix after strings.TrimSpace, but
			// that helper concatenated a transcript entry's blocks into one string,
			// so leading whitespace was reachable there. Matching per BLOCK here
			// makes it unreachable, and all 31 corpus occurrences are the bare
			// bracketed string with nothing before it — so a trim would be tolerance
			// bought with no observation behind it.
			name: "a leading space puts the notice back in the unrecognized lane",
			line: syntheticUserLine("", textBlock(" "+interruptNoticeShortFixture)),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  textBlock(" " + interruptNoticeShortFixture),
			}},
		},
		{
			// NO SUBSTRING. A Contains would swallow any block that merely mentions
			// the notice — a person quoting it back, or a future harness payload
			// that embeds it in a longer narration.
			name: "the notice mid-string does not suppress",
			line: syntheticUserLine("", textBlock("quoting it back: "+interruptNoticeShortFixture+" — why?")),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "text",
				Raw:  textBlock("quoting it back: " + interruptNoticeShortFixture + " — why?"),
			}},
		},
		{
			// THE TRUST BOUNDARY, mirroring the flag arm's own row. A genuinely new
			// block type is the alarm the unrecognized lane exists to raise, and a
			// third trigger must not blanket it. Hostile tool output cannot reach
			// the text arm either — a tool_result's payload decodes into Content,
			// never Text — so tripping this guard takes control of the block's TYPE,
			// not just of a string.
			name: "an unknown block type beside the notice still surfaces",
			line: syntheticUserLine("",
				textBlock(interruptNoticeShortFixture)+`,{"type":"image","text":"ignored"}`),
			want: []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedUserBlock,
				Kind: "image",
				Raw:  `{"type":"image","text":"ignored"}`,
			}},
		},
		{
			// Scope is emitUser. The same bytes on an ASSISTANT line are model
			// speech and still map — a guard placed in decodeBlock or emitAssistant
			// would drop what the person came to read. Mirrors the nudge's and the
			// flag's own assistant-surface rows.
			name: "the notice on an assistant line does not suppress model speech",
			line: `{"type":"assistant","message":{"id":"msg-1611","role":"assistant","content":[` +
				textBlock(interruptNoticeShortFixture) + `]}}`,
			want: []turnevent.Event{turnevent.TextChunk{
				MessageID: "msg-1611",
				Text:      interruptNoticeShortFixture,
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("events for %s:\ngot  %#v\nwant %#v", tc.name, got, tc.want)
			}
		})
	}
}

// TestParser_InterruptNoticeDropIsLoggedContentFree is #1611 AC1's logging half,
// in the shape TestParser_HarnessNudgeDropIsLoggedContentFree has: the drop is
// Debug-logged with site and block type ONLY, at the SAME site as the two existing
// triggers, and never with the text.
//
// Deliberately the same message and the same exactly-two attrs, which is what
// makes this test also assert the absence of a `trigger` attribute naming which of
// the three arms fired. That attribute is unavailable on the merits — the nudge is
// itself flagged, so it could not tell a nudge from a skill body — and ruled out
// on disclosure grounds at the drop site, because one careless attr there writes
// an 87244-char skill body into the daemon's logs.
//
// The third assertion is the load-bearing one for the same reason it is in the
// nudge's test: the first two only describe what is present, while the sweep is
// what catches someone appending "text", block.Text later.
func TestParser_InterruptNoticeDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))

	line := syntheticUserLine("", textBlock(interruptNoticeToolUseFixture))
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	drops := rec.withMessage(harnessNudgeDropMsg)
	if len(drops) != 1 {
		t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)", harnessNudgeDropMsg, len(drops), rec.all())
	}
	wantAttrs := map[string]string{
		"site": string(turnevent.UnrecognizedUserBlock),
		"type": "text",
	}
	if !reflect.DeepEqual(drops[0].attrs, wantAttrs) {
		t.Errorf("drop attrs: got %v, want exactly %v", drops[0].attrs, wantAttrs)
	}
	for _, r := range rec.all() {
		for k, v := range r.attrs {
			if strings.Contains(v, interruptNoticeToolUseFixture) {
				t.Errorf("record %q attr %q carries the block text; the drop site logs site and type only", r.msg, k)
			}
		}
		if strings.Contains(r.msg, interruptNoticeToolUseFixture) {
			t.Errorf("record message carries the block text: %q", r.msg)
		}
	}
}

// TestParser_CapturedSyntheticUserLinePinsTheWireSpelling is #2087 AC2: the
// matcher is proven against REAL CAPTURED BYTES, inside `make check`.
//
// A hand-written line cannot do this job. The whole risk of keying on a flag is
// that the decoder is keyed on a spelling claude does not send on THIS surface,
// in which case it compiles, decodes nothing, and never fires — and a hermetic
// fixture written from the same wrong guess would agree with it and pass. #2023
// paid for that lesson once (`tool_use_result` vs `toolUseResult`), and the
// transcript's `isMeta` is the same trap set again.
//
// The capture is claude's verbatim stdout at 2.1.220, committed under the
// realclaude testdata directory but read from here WITHOUT the e2e_realclaude
// build tag, so this proof runs in the standard gate rather than behind an opt-in
// suite that exits 0 with no credentials.
//
// The swap is the load-bearing half. Replaying the captured line unmodified
// proves nothing about the flag: those exact bytes ALSO carry the nudge, so the
// pre-#2087 parser drops them too. Swapping the text for a non-nudge probe
// removes the nudge arm from the picture and leaves the flag as the only thing
// that can still produce silence.
func TestParser_CapturedSyntheticUserLinePinsTheWireSpelling(t *testing.T) {
	t.Parallel()
	captured := capturedLine(t, "user", "")

	// The spelling pin itself. Named separately from the replay so a claude
	// version that renames the key fails HERE, with a message that says which
	// spellings are the known decoys, rather than as a puzzling Unrecognized.
	if !bytes.Contains(captured, []byte(syntheticFlagFixture)) {
		t.Fatalf("the captured user line does not carry %s.\n"+
			"This surface's spelling is what userLine.IsSynthetic is keyed on; the JSONL "+
			"transcript's %q and the over-generalised %q are both dead code here. "+
			"Re-read the capture before changing the struct tag.\nline: %s",
			syntheticFlagFixture, "isMeta", "is_synthetic", captured)
	}

	if got := collectEvents(string(captured)); got != nil {
		t.Errorf("the captured synthetic user line emitted %d event(s), want 0: %#v", len(got), got)
	}

	swapped := strings.Replace(string(captured), harnessNudgeFixture, syntheticSkillProbe, 1)
	if swapped == string(captured) {
		t.Fatalf("the captured line does not contain the nudge fixture, so the swap did "+
			"nothing and the flag arm is untested. Either the capture changed or "+
			"harnessNudgeFixture drifted from it.\nline: %s", captured)
	}
	if got := collectEvents(swapped); got != nil {
		t.Errorf("the captured line with its text swapped for a non-nudge probe emitted "+
			"%d event(s), want 0.\nThe nudge constant cannot be what fired here, so this "+
			"is the flag arm — and it did not: %#v", len(got), got)
	}
}

// TestParser_SyntheticDropIsLoggedContentFree is #2087 AC1's logging half, and
// the sibling of TestParser_HarnessNudgeDropIsLoggedContentFree.
//
// It matters more on this arm than on the nudge's. The nudge is 100 bytes of
// known boilerplate; a flagged block is a skill body, up to tens of kilobytes of
// operator-authored instruction that can name internal procedure. Appending
// "text", block.Text to the drop site would write all of it to the daemon's
// stderr and journald — reintroducing the disclosure this ticket removes, one
// layer down, where nobody is looking.
func TestParser_SyntheticDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))

	line := syntheticUserLine(syntheticFlagFixture, textBlock(syntheticSkillProbe))
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	drops := rec.withMessage(harnessNudgeDropMsg)
	if len(drops) != 1 {
		t.Fatalf("records with message %q: got %d, want 1 (all records: %+v)", harnessNudgeDropMsg, len(drops), rec.all())
	}
	wantAttrs := map[string]string{
		"site": string(turnevent.UnrecognizedUserBlock),
		"type": "text",
	}
	if !reflect.DeepEqual(drops[0].attrs, wantAttrs) {
		t.Errorf("drop attrs: got %v, want exactly %v", drops[0].attrs, wantAttrs)
	}
	for _, r := range rec.all() {
		for k, v := range r.attrs {
			if strings.Contains(v, syntheticSkillProbe) {
				t.Errorf("record %q attr %q carries the block text; the drop site logs site and type only", r.msg, k)
			}
		}
		if strings.Contains(r.msg, syntheticSkillProbe) {
			t.Errorf("record message carries the block text: %q", r.msg)
		}
	}
}
