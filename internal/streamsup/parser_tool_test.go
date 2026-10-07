package streamsup

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// --- tool_use_result sidecar (#2024) ---------------------------------------
//
// FIXTURE PROVENANCE. Every line below is built from committed observed bytes,
// never hand-typed from a table: a fixture composed from a ticket's prose proves
// the composer agrees with the ticket, not with claude.
//
// The two lines that matter most were observed on THE SURFACE THIS DECODER
// READS — claude's stdout, `--output-format stream-json`, claude 2.1.239 — and
// are transcribed from the retained user lines of the committed capture
// internal/e2e/realclaude/testdata/tool_result_sidecar_v2.1.239.json. They are
// inlined here with that citation rather than read across the package boundary
// at test time, per this repo's practice. The `$WORKDIR` / `$SESSION_ID`
// placeholders are the capture's own redaction markers, kept verbatim.
//
// THE ENVELOPE KEY IS THE WHOLE POINT. On stdout the sidecar is spelled
// `tool_use_result`; the TRANSCRIPT spells the same payload `toolUseResult`.
// A decoder keyed on the camelCase name is dead code here, and a fixture lifted
// whole from a transcript line would carry that same wrong key and agree with it
// — green, and proving nothing. Where a fixture below is transcript-derived, the
// `file` object's bytes are taken and re-wrapped in a stdout-shaped line, never
// the line around them. Note also that the rename stops AT the envelope: every
// key inside stays camelCase (numLines, totalLines, filePath).

// sidecarReadEqualLine is the first retained user line of the committed stdout
// capture: a Read whose returned and total counts are both 4.
const sidecarReadEqualLine = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01QGE5CxkESTinJMgTZzMCdE","type":"tool_result","content":"1\tpyry #2023 probe input.\n2\tThis file exists so one Read tool call has something to return.\n3\tIts bytes are a declared constant in tool_result_sidecar_probe_test.go.\n4\t"}]},"parent_tool_use_id":null,"session_id":"$SESSION_ID","uuid":"0802d929-8014-4f34-8c0c-9c4dd4425eff","timestamp":"2026-09-02T11:07:58.537Z","tool_use_result":{"type":"text","file":{"filePath":"$WORKDIR/sidecap-input.txt","content":"pyry #2023 probe input.\nThis file exists so one Read tool call has something to return.\nIts bytes are a declared constant in tool_result_sidecar_probe_test.go.\n","numLines":4,"startLine":1,"totalLines":4}}}`

// sidecarReadEqualContent is that line's tool_result block content, unescaped —
// the text the existing mapping already carried before this ticket.
const sidecarReadEqualContent = "1\tpyry #2023 probe input.\n2\tThis file exists so one Read tool call has something to return.\n3\tIts bytes are a declared constant in tool_result_sidecar_probe_test.go.\n4\t"

// sidecarShellLine is the second retained user line of the same capture: a Bash
// call whose sidecar is a well-formed object carrying no read keys at all. Its
// key set (stdout/stderr/interrupted/isImage/noOutputExpected) is the observed
// proof that "an object that is not a read" is an ordinary, frequent shape
// rather than a hypothetical.
const sidecarShellLine = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01Dnb1t3eG5ZLkyXpYDvD3fW","type":"tool_result","content":"pyry-2023-shell-marker","is_error":false}]},"parent_tool_use_id":null,"session_id":"$SESSION_ID","uuid":"9a2ef321-a3df-4166-82a7-07c3aa9daabe","timestamp":"2026-09-02T11:08:00.102Z","tool_use_result":{"stdout":"pyry-2023-shell-marker","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}}`

// sidecarLine wraps one sidecar VALUE in a stdout-shaped user line carrying a
// single tool_result block. Used for the transcript-derived arms and the
// adversarial ones, so each case states only the bytes under test.
func sidecarLine(toolUseID, sidecar string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` +
		toolUseID + `","content":"x","is_error":false}]},"tool_use_result":` + sidecar + `}`
}

// readSidecar builds a read-shaped sidecar with the two counts written as raw
// JSON, so a case can put a non-integer or out-of-range token where a number
// belongs. startLine and filePath are present because the observed shape carries
// them; the read sidecar is ignored when composing details.
func readSidecar(numLines, totalLines string) string {
	return `{"type":"text","file":{"filePath":"/tmp/x.go","content":"irrelevant","numLines":` +
		numLines + `,"startLine":1,"totalLines":` + totalLines + `}}`
}

// TestParser_SidecarReadSendsNoCount keeps read rows free of trailing counts.
func TestParser_SidecarReadSendsNoCount(t *testing.T) {
	t.Parallel()
	t.Run("captured stdout preserves result content", func(t *testing.T) {
		t.Parallel()
		got := collectEvents(sidecarReadEqualLine)
		want := []turnevent.Event{turnevent.ToolUpdate{
			ToolCallID: "toolu_01QGE5CxkESTinJMgTZzMCdE",
			Status:     turnevent.ToolStatusCompleted,
			Content:    turnevent.TextContent{Text: sidecarReadEqualContent},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("events:\n got: %#v\nwant: %#v", got, want)
		}
	})
	tests := []struct {
		name, sidecar string
	}{
		{"equal counts", readSidecar("4", "4")},
		{"partial read", readSidecar("40", "256")},
		{"offset read", `{"type":"text","file":{"numLines":50,"startLine":155,"totalLines":236}}`},
		{"zero returned", readSidecar("0", "1676")},
		{"returned above total", readSidecar("300", "256")},
		{"empty file", readSidecar("0", "0")},
		{"numLines alone", `{"file":{"numLines":40}}`},
		{"totalLines alone", `{"file":{"totalLines":256}}`},
		{"fractional count", readSidecar("4.5", "256")},
		{"stringified count", readSidecar(`"40"`, "256")},
		{"count past int64", readSidecar("99999999999999999999999999", "256")},
		{"negative count", readSidecar("-1", "256")},
		{"negative total", readSidecar("40", "-256")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantDetail(t, sidecarLine("tu-read", tc.sidecar), "")
		})
	}
}

// TestParser_SidecarFailsClosed covers AC3: an absent sidecar, a NON-OBJECT
// sidecar, and an object carrying read-shaped nothing all send no count, none
// fails the line's decode, and — the half that is easy to lose — none produces a
// second unrecognized outcome.
//
// That last clause is a CONFINEMENT control, not a tidiness one. emitUnrecognized
// does not merely log: it puts the offending bytes on the wire as
// turnevent.Unrecognized.Raw, cut by truncateRaw. A design that surfaced an
// unrecognised sidecar that way would ship the first maxUnrecognizedRaw bytes of
// every file claude reads to the phone. Asserting the WHOLE event list is what
// keeps that shut — an extra Unrecognized makes the list differ.
func TestParser_SidecarFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
	}{
		{
			// The pre-#2024 shape: every line the daemon has ever parsed.
			name: "absent sidecar",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-absent","content":"x","is_error":false}]}}`,
		},
		{
			// Observed on a captured teardown (probeToolResultTeardownAbort,
			// claude 2.1.220): the sidecar is a BARE STRING, not an object. Named
			// by its value because that value is the evidence — a decoder assuming
			// an object would have to survive exactly this.
			name: `non-object sidecar: the bare string "Error: Exit code 1"`,
			line: sidecarLine("tu-bare", `"Error: Exit code 1"`),
		},
		{
			name: "null sidecar",
			line: sidecarLine("tu-null", `null`),
		},
		{
			name: "numeric sidecar",
			line: sidecarLine("tu-num", `1676`),
		},
		{
			// `file` present but not an object — the shape a decoder that checked
			// only for the key's presence would trip over.
			name: "file is not an object",
			line: sidecarLine("tu-filestr", `{"type":"text","file":"nope"}`),
		},
		{
			// A read sidecar's own `type` is "text" and a write's is "create", so
			// `type` identifies nothing. This is the shape that would send a count
			// if the arm were keyed on it.
			name: "read-shaped type with no file object",
			line: sidecarLine("tu-typeonly", `{"type":"text"}`),
		},
		{
			name: "empty object",
			line: sidecarLine("tu-empty", `{}`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(tc.line)
			if len(got) != 1 {
				t.Fatalf("want exactly one event (no second unrecognized outcome), got %d: %#v", len(got), got)
			}
			upd, ok := got[0].(turnevent.ToolUpdate)
			if !ok {
				t.Fatalf("want a ToolUpdate, got %#v", got[0])
			}
			if upd.ResultDetail != "" {
				t.Errorf("ResultDetail: got %q, want empty", upd.ResultDetail)
			}
		})
	}
}

// TestParser_SidecarMultiBlockSendsNoCount covers AC4. One line carries one
// sidecar; a user message may carry many tool_result blocks, and the sidecar
// cannot be attributed to a particular one. A count on the wrong row is worse
// than no count, so more than one block drops it from ALL of them.
//
// The single-block control on the same sidecar bytes is what keeps this from
// being vacuously green: without it, a decoder that never composed anything at
// all would pass the multi-block half.
func TestParser_SidecarMultiBlockSendsNoCount(t *testing.T) {
	t.Parallel()
	const sidecar = `,"tool_use_result":{"oldString":"before","newString":"after","structuredPatch":[{"lines":["+a","-r"]}]}`
	block := func(id string) string {
		return `{"type":"tool_result","tool_use_id":"` + id + `","content":"x","is_error":false}`
	}

	t.Run("one block carries the count", func(t *testing.T) {
		t.Parallel()
		got := collectEvents(`{"type":"user","message":{"role":"user","content":[` + block("tu-solo") + `]}` + sidecar + `}`)
		want := []turnevent.Event{turnevent.ToolUpdate{
			ToolCallID:   "tu-solo",
			Status:       turnevent.ToolStatusCompleted,
			Content:      turnevent.TextContent{Text: "x"},
			ResultDetail: "+1 " + minusGlyph + "1",
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("control case:\n got: %#v\nwant: %#v", got, want)
		}
	})

	t.Run("two blocks carry no count on either", func(t *testing.T) {
		t.Parallel()
		got := collectEvents(`{"type":"user","message":{"role":"user","content":[` +
			block("tu-a") + `,` + block("tu-b") + `]}` + sidecar + `}`)
		want := []turnevent.Event{
			turnevent.ToolUpdate{ToolCallID: "tu-a", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "x"}},
			turnevent.ToolUpdate{ToolCallID: "tu-b", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "x"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("multi-block case:\n got: %#v\nwant: %#v", got, want)
		}
	})
}

// TestParser_SidecarContentIsConfined covers AC1's second half. The read
// sidecar's file.content is THE ENTIRE CONTENTS OF THE FILE CLAUDE READ, and
// file.filePath is an absolute path disclosing the operator's layout. Neither
// may reach the event or the wire.
//
// The guarantee is structural — neither field is declared in the decode target,
// and a field that does not exist cannot leak — but the assertion is made
// against the marshalled event anyway, because "we didn't declare it" is exactly
// the kind of claim that survives a refactor in prose while failing in fact.
//
// The markers are non-empty and distinctive on purpose: strings.Contains(s, "")
// is true for every s, so an empty needle would make this check green
// unconditionally.
func TestParser_SidecarContentIsConfined(t *testing.T) {
	t.Parallel()
	const contentMarker = "PYRY-2024-SECRET-FILE-CONTENT-MARKER"
	const pathMarker = "PYRY-2024-SECRET-PATH-MARKER"

	line := sidecarLine("tu-confine", `{"type":"text","file":{"filePath":"/home/`+pathMarker+
		`/x.go","content":"`+contentMarker+`","numLines":40,"startLine":1,"totalLines":256}}`)

	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("want exactly one event, got %d: %#v", len(got), got)
	}
	upd, ok := got[0].(turnevent.ToolUpdate)
	if !ok {
		t.Fatalf("want a ToolUpdate, got %#v", got[0])
	}
	// Read details are suppressed, and the original sidecar stays confined.
	if upd.ResultDetail != "" {
		t.Errorf("ResultDetail: got %q, want empty", upd.ResultDetail)
	}
	blob, err := json.Marshal(upd)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	for _, marker := range []string{contentMarker, pathMarker} {
		if strings.Contains(string(blob), marker) {
			t.Errorf("sidecar marker %q leaked into the event: %s", marker, blob)
		}
	}
}

// TestStreamLine_StaysSegmentationOnly covers AC1's first half. streamLine is
// the line-level SEGMENTATION struct; systemTaskStartedLine's doc states that
// fields belonging to a single line type "would blur that boundary". The sidecar
// is decoded a SECOND time off the raw line bytes instead, so this asserts the
// struct did not widen — the cheapest way to make "we decoded twice rather than
// widening" a fact rather than a claim in a commit message.
func TestStreamLine_StaysSegmentationOnly(t *testing.T) {
	t.Parallel()
	var want = []string{"Type", "Subtype", "Message"}
	rt := reflect.TypeOf(streamLine{})
	var got []string
	for i := range rt.NumField() {
		got = append(got, rt.Field(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("streamLine fields: got %v, want %v — the sidecar belongs in its own decode target, not here", got, want)
	}
}

// TestToolResultDetail_BoundedByConstruction checks the remaining forms against
// the conservative byte allowance and pins their non-ASCII separator glyphs.
func TestToolResultDetail_BoundedByConstruction(t *testing.T) {
	t.Parallel()
	forms := map[string]string{
		"edit":  toolResultDetail(json.RawMessage(editSidecar(`[{"lines":["+a","-r"]}]`))),
		"write": toolResultDetail(json.RawMessage(writeSidecar("update", ``, `[]`))),
	}
	for name, got := range forms {
		if got == "" {
			t.Errorf("%s form composed nothing", name)
		}
		if len(got) >= maxResultDetailBytes {
			t.Errorf("%s form is %d bytes (%q), want < %d", name, len(got), got, maxResultDetailBytes)
		}
		for _, r := range got {
			if r > unicode.MaxASCII && r != '−' && r != '·' {
				t.Errorf("%s form emits unexpected non-ASCII rune %q", name, r)
			}
		}
	}
	// Each non-negative int64 formats to at most 19 digits. These upper bounds
	// include the multi-byte separators, even though parse-buffer limits keep
	// the counted inputs too small to reach them.
	for name, n := range map[string]int{
		"edit":  len("+") + 19 + len(" ") + len(minusGlyph) + 19,
		"write": len("updated") + 1 + len(dotGlyph) + 1 + 19 + len(" lines"),
	} {
		if n >= maxResultDetailBytes {
			t.Errorf("derived %s bound is %d bytes, want < %d", name, n, maxResultDetailBytes)
		}
	}
}

// --- tool_use_result sidecar: the remaining four shapes (#2025) --------------
//
// FIXTURE PROVENANCE DIFFERS PER SHAPE, AND ONLY TWO OF THE FIVE ARE OBSERVED ON
// STDOUT. The block above states the general rule; this one states where each of
// these four stands, because pretending they are equally well evidenced is the
// mistake that would make a wrong guess look like a measurement:
//
//   - shell — sidecarShellLine, already inlined above from the committed stdout
//     capture. OBSERVED ON THE SURFACE THIS DECODER READS.
//   - write — the nested sidecar object of internal/agentrun/jsonl/testdata/
//     clean.jsonl, RE-WRAPPED under the snake_case `tool_use_result` envelope.
//     A transcript line lifted whole carries `toolUseResult` and would exercise
//     nothing. Its key set, `type:"create"` and `structuredPatch:[]` are the
//     observed bytes; its 27686-byte content is replaced by a short stand-in,
//     since the count is computed by the daemon rather than observed.
//   - edit, search — NO COMMITTED EVIDENCE ON EITHER SURFACE. Authored from
//     #1794's census (re-measured 2026-09-02 over 7327 sidecars) and marked
//     AUTHORED-FROM-CENSUS so a later capture can confirm them. Deliberate: the
//     two shapes checked on both surfaces were byte-identical, #2023 established
//     that the rename stops at the envelope, and the fail-closed default means a
//     wrong guess here costs a missing count and never a wrong one.
//
// The nested keys are camelCase on both surfaces, exactly as spelled below.

// minusGlyph and dotGlyph are the row separators, written as escapes so a test
// expectation cannot silently agree with an ASCII hyphen typed into the
// composer. They are display text the client renders verbatim, so they are part
// of the contract rather than a formatting preference.
const (
	minusGlyph = "−" // U+2212 MINUS SIGN — NOT U+002D HYPHEN-MINUS
	dotGlyph   = "·" // U+00B7 MIDDLE DOT, spaced on both sides
)

// shellSidecar builds the observed shell key set. extra is spliced in raw so a
// case can carry one of the 5.9% of observed sixth keys.
func shellSidecar(stdout, stderr, extra string) string {
	return `{"stdout":"` + stdout + `","stderr":"` + stderr +
		`","interrupted":false,"isImage":false,"noOutputExpected":false` + extra + `}`
}

// editSidecar builds the observed edit key set around raw hunk JSON. filePath,
// originalFile, oldString and newString are present because the observed shape
// carries them and because oldString/newString are what IDENTIFY an edit; none
// of the four is decoded.
func editSidecar(hunks string) string {
	return `{"filePath":"/tmp/x.go","oldString":"before","newString":"after",` +
		`"originalFile":"the whole pre-edit file","replaceAll":false,` +
		`"userModified":false,"structuredPatch":` + hunks + `}`
}

// writeSidecar builds the observed write key set. structuredPatch is [] by
// default because that is what every one of the 240 observed creates carries.
func writeSidecar(typ, content, patch string) string {
	return `{"type":"` + typ + `","filePath":"/tmp/x.go","content":"` + content +
		`","originalFile":null,"userModified":false,"structuredPatch":` + patch + `}`
}

// searchSidecar builds the observed search key set around raw count JSON, so a
// case can omit a count or put a negative where a count belongs.
func searchSidecar(counts string) string {
	return `{"mode":"content","filenames":["/tmp/a.go","/tmp/b.go"],` +
		`"content":"the whole grep output"` + counts + `}`
}

// wantDetail asserts that one line yields exactly one ToolUpdate carrying want.
// Exactly one event is the assertion that keeps the no-Unrecognized rule shut:
// an extra event makes the count differ rather than hiding inside a field
// comparison.
func wantDetail(t *testing.T, line, want string) {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("want exactly one event (no second unrecognized outcome), got %d: %#v", len(got), got)
	}
	upd, ok := got[0].(turnevent.ToolUpdate)
	if !ok {
		t.Fatalf("want a ToolUpdate, got %#v", got[0])
	}
	if upd.ResultDetail != want {
		t.Errorf("ResultDetail: got %q, want %q", upd.ResultDetail, want)
	}
}

// TestParser_SidecarShellSendsNoCount suppresses both stdout and stderr counts.
func TestParser_SidecarShellSendsNoCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, line string
	}{
		{"captured stdout", sidecarShellLine},
		{"longer stderr", sidecarLine("tu-stderr", shellSidecar(`a\nb\n`, `e1\ne2\ne3\ne4\ne5\ne6`, ``))},
		{"extra key", sidecarLine("tu-gitop", shellSidecar(`a\nb\nc`, ``, `,"gitOperation":true`))},
		{"trailing newline", sidecarLine("tu-trail", shellSidecar(`a\nb\nc\n`, ``, ``))},
		{"empty stdout", sidecarLine("tu-noout", shellSidecar(``, `something on stderr`, ``))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantDetail(t, tc.line, "")
		})
	}
}

// TestParser_SidecarEditCounts covers AC2. AUTHORED FROM CENSUS.
//
// The two numbers can ONLY come from the +/- prefixes of the hunks' `lines`.
// A hunk is {oldStart, oldLines, newStart, newLines, lines} and oldLines /
// newLines are the hunk's SPANS, context included — differencing them yields the
// net change, not "+10 −3". Every hunk below therefore carries spans that a
// span-differencing composer would turn into a different, plausible answer.
func TestParser_SidecarEditCounts(t *testing.T) {
	t.Parallel()
	// Four context lines against three changed ones, with spans 5 and 6. A
	// composer differencing the spans reports "+1"; the prefixes say "+2 −1".
	const contextHeavy = `[{"oldStart":1,"oldLines":5,"newStart":1,"newLines":6,` +
		`"lines":[" ctx1"," ctx2","+add1","+add2","-rem1"," ctx3"," ctx4"]}]`
	tests := []struct {
		name, line, want string
	}{
		{
			name: "context lines outnumber changed ones and the prefixes still decide",
			line: sidecarLine("tu-edit", editSidecar(contextHeavy)),
			want: "+2 " + minusGlyph + "1",
		},
		{
			// Two hunks, so a composer reading only the first is caught.
			name: "counts sum across hunks",
			line: sidecarLine("tu-edit2", editSidecar(
				`[{"oldStart":1,"oldLines":2,"newStart":1,"newLines":2,"lines":[" c","-r1","+a1"]},`+
					`{"oldStart":9,"oldLines":3,"newStart":9,"newLines":4,"lines":[" c","+a2","-r2","-r3"]}]`)),
			want: "+2 " + minusGlyph + "3",
		},
		{
			// 190 of 632 observed edits added only. The empty half is OMITTED, not
			// rendered as a zero.
			name: "additions only omit the removed half",
			line: sidecarLine("tu-addonly", editSidecar(
				`[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":4,"lines":[" c","+a1","+a2","+a3"]}]`)),
			want: "+3",
		},
		{
			name: "removals only omit the added half",
			line: sidecarLine("tu-remonly", editSidecar(
				`[{"oldStart":1,"oldLines":3,"newStart":1,"newLines":1,"lines":[" c","-r1","-r2"]}]`)),
			want: minusGlyph + "2",
		},
		{
			// None of the 632 observed edits had both halves zero, so this is a
			// fail-closed decision about an unobserved shape rather than a measured
			// one — a context-only patch changed nothing worth reporting.
			name: "both halves zero send no count",
			line: sidecarLine("tu-editzero", editSidecar(
				`[{"oldStart":1,"oldLines":2,"newStart":1,"newLines":2,"lines":[" c1"," c2"]}]`)),
			want: "",
		},
		{
			// An empty patch is not an edit's shape — every observed edit carries
			// hunks; it is the WRITE shape that carries []. Nothing to count.
			name: "an empty structuredPatch sends no count",
			line: sidecarLine("tu-editempty", editSidecar(`[]`)),
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantDetail(t, tc.line, tc.want)
		})
	}
}

// TestParser_SidecarEditGlyphIsMinusSign pins the separator to U+2212 rather
// than to whatever the composer happens to emit. A hyphen would read almost
// identically in a diff and in a terminal, and the client renders this text
// verbatim, so the codepoint is contract.
func TestParser_SidecarEditGlyphIsMinusSign(t *testing.T) {
	t.Parallel()
	got := collectEvents(sidecarLine("tu-glyph", editSidecar(
		`[{"oldStart":1,"oldLines":2,"newStart":1,"newLines":2,"lines":["+a","-r"]}]`)))
	if len(got) != 1 {
		t.Fatalf("want exactly one event, got %d: %#v", len(got), got)
	}
	detail := got[0].(turnevent.ToolUpdate).ResultDetail
	if strings.ContainsRune(detail, '-') {
		t.Errorf("detail %q carries U+002D HYPHEN-MINUS; the contract is U+2212 MINUS SIGN", detail)
	}
	if !strings.ContainsRune(detail, '−') {
		t.Errorf("detail %q carries no U+2212 MINUS SIGN", detail)
	}
	if !strings.ContainsRune(detail, '+') {
		t.Errorf("detail %q carries no U+002B PLUS SIGN", detail)
	}
}

// TestParser_SidecarWriteCounts covers AC3.
//
// structuredPatch is [] on EVERY observed create — all 240 of them — so an arm
// requiring a non-empty patch to recognise a write is dead code on 97% of
// writes, and a fixture built from a create would agree with it. Present-but-
// empty must be distinguishable from absent, which jsonKey preserves.
func TestParser_SidecarWriteCounts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, line, want string
	}{
		{
			// The observed shape, from clean.jsonl: type "create", patch [].
			name: "observed create with an empty structuredPatch sends the verb and the count",
			line: sidecarLine("tu-create", writeSidecar("create", `l1\nl2\nl3\n`, `[]`)),
			want: "created " + dotGlyph + " 3 lines",
		},
		{
			name: "update with hunks sends the update verb",
			line: sidecarLine("tu-update", writeSidecar("update", `l1\nl2`,
				`[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":2,"lines":[" l1","+l2"]}]`)),
			want: "updated " + dotGlyph + " 2 lines",
		},
		{
			// The verb still names the creation of an empty file.
			name: "empty content still names the verb",
			line: sidecarLine("tu-emptyfile", writeSidecar("create", ``, `[]`)),
			want: "created " + dotGlyph + " 0 lines",
		},
		{
			name: "a trailing newline does not add a line",
			line: sidecarLine("tu-wtrail", writeSidecar("update", `l1\nl2`, `[]`)),
			want: "updated " + dotGlyph + " 2 lines",
		},
		{
			name: "a type that is neither create nor update sends no count",
			line: sidecarLine("tu-delete", writeSidecar("delete", `l1\nl2`, `[]`)),
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantDetail(t, tc.line, tc.want)
		})
	}
}

// TestParser_SidecarSearchSendsNoCount suppresses Grep and Glob result counts.
func TestParser_SidecarSearchSendsNoCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, sidecar string
	}{
		{"Grep line and file counts", searchSidecar(`,"numLines":78,"numFiles":0,"totalLines":120`)},
		{"Grep file count", searchSidecar(`,"numFiles":5,"totalFiles":5`)},
		{"Grep partial results", searchSidecar(`,"numLines":17,"totalLines":900`)},
		{"no count", searchSidecar(`,"totalFiles":5`)},
		{"negative line count", searchSidecar(`,"numLines":-1,"numFiles":5`)},
		{"Glob file count", `{"filenames":["/tmp/a.go"],"numFiles":1,"truncated":false}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantDetail(t, sidecarLine("tu-search", tc.sidecar), "")
		})
	}
}

// TestParser_SidecarPartialShapesFailClosed covers AC5: the fail-closed default
// #2024 established still holds for a shape that is recognised IN PART. Each
// case matches some of an arm's keys and none of it completely.
func TestParser_SidecarPartialShapesFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, line string
	}{
		{
			// structuredPatch present, but neither the edit's oldString/newString
			// nor the write's type+content. The two patch arms are separated by
			// their OTHER keys, not by their `type`.
			name: "structuredPatch matching neither patch arm",
			line: sidecarLine("tu-patchonly",
				`{"filePath":"/tmp/x.go","structuredPatch":[{"oldStart":1,"oldLines":1,`+
					`"newStart":1,"newLines":2,"lines":["+a"]}]}`),
		},
		{
			// A write's type and content with no structuredPatch at all: all three
			// keys are required, and absent is not the same as [].
			name: "write keys with no structuredPatch",
			line: sidecarLine("tu-nopatch", `{"type":"create","content":"a\nb","filePath":"/tmp/x.go"}`),
		},
		{
			// An edit's discriminators with no patch to count.
			name: "edit keys with no structuredPatch",
			line: sidecarLine("tu-noedit", `{"oldString":"a","newString":"b","filePath":"/tmp/x.go"}`),
		},
		{
			// stdout with no interrupted: one key of the shell triple is not the
			// shape, and this is the case a single-key arm would wrongly claim.
			name: "stdout with no interrupted is not a shell call",
			line: sidecarLine("tu-halfshell", `{"stdout":"a\nb","isImage":false}`),
		},
		{
			// From the census's no-count row: a real observed shape that resembles
			// a search without being one.
			name: "an observed matches/query shape",
			line: sidecarLine("tu-matches", `{"matches":[],"query":"foo"}`),
		},
		{
			// Also from the no-count row.
			name: "an observed commandName/success shape",
			line: sidecarLine("tu-cmd", `{"commandName":"/clear","success":true}`),
		},
		{
			// 237 of 7327 observed sidecars are a bare string or an array. The
			// array half had no case before this ticket.
			name: "an array sidecar",
			line: sidecarLine("tu-arr", `[{"stdout":"a\nb","interrupted":false}]`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantDetail(t, tc.line, "")
		})
	}
}

// TestParser_SidecarShapesAreConfined extends #2024's confinement assertion to
// the four shapes this ticket adds, and they carry far more to exclude than the
// read did: originalFile is THE ENTIRE PRE-EDIT FILE, filePath and filenames are
// absolute paths disclosing the operator's layout, a search sidecar's content is
// the whole grep output, and stderr is unbounded claude output.
//
// The guarantee is structural — none of those is declared on any decode target,
// and a field that does not exist cannot leak — but asserting it against the
// marshalled event anyway is the point: "we didn't declare it" is exactly the
// claim that survives a refactor in prose while failing in fact.
//
// Every marker is non-empty and distinctive because strings.Contains(s, "") is
// true for every s, which would make this check green unconditionally.
func TestParser_SidecarShapesAreConfined(t *testing.T) {
	t.Parallel()
	const m = "PYRY-2025-SECRET-MARKER"
	tests := []struct {
		name, line, wantDetail string
	}{
		{
			name:       "shell stderr is neither counted nor carried",
			line:       sidecarLine("tu-c1", shellSidecar(`a\nb`, m, ``)),
			wantDetail: "",
		},
		{
			name: "edit originalFile, filePath and hunk text stay out",
			line: sidecarLine("tu-c2", `{"filePath":"/home/`+m+`/x.go","oldString":"`+m+
				`","newString":"`+m+`","originalFile":"`+m+`","structuredPatch":`+
				`[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":2,"lines":["+`+m+`"]}]}`),
			wantDetail: "+1",
		},
		{
			name:       "write content and filePath stay out",
			line:       sidecarLine("tu-c3", writeSidecar("create", m+`\n`+m, `[]`)),
			wantDetail: "created " + dotGlyph + " 2 lines",
		},
		{
			name: "search filenames and grep output stay out",
			line: sidecarLine("tu-c4", `{"mode":"content","filenames":["/home/`+m+
				`/a.go"],"content":"`+m+`","numLines":9,"numFiles":1}`),
			wantDetail: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectEvents(tc.line)
			if len(got) != 1 {
				t.Fatalf("want exactly one event, got %d: %#v", len(got), got)
			}
			upd, ok := got[0].(turnevent.ToolUpdate)
			if !ok {
				t.Fatalf("want a ToolUpdate, got %#v", got[0])
			}
			// Supported counts still land while all sidecar text stays confined.
			if upd.ResultDetail != tc.wantDetail {
				t.Errorf("ResultDetail: got %q, want %q", upd.ResultDetail, tc.wantDetail)
			}
			blob, err := json.Marshal(upd)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			if strings.Contains(string(blob), m) {
				t.Errorf("sidecar marker leaked into the event: %s", blob)
			}
		})
	}
}
