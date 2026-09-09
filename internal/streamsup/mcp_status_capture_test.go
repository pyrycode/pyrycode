package streamsup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
)

// mcpStatusCapturePath is the committed real-claude capture of what one live
// claude answers to `control_request{subtype:"mcp_status"}` with a deliberately
// broken MCP server in its `--mcp-config` document (#2272), produced by
// internal/e2e/realclaude's TestRealClaude_MCPStatusCapture.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// compactionCapturePath gives: the bytes are only BYTES, the e2e_realclaude build
// tag belongs to that package's Go files rather than to its testdata, and reading
// it from here is what keeps the measurement inside `make check` instead of behind
// an opt-in gate that SKIPS (exit 0) with no claude login.
//
// THIS IS ANOTHER READER, NOT A GENERALISATION OF capturedLines,
// capturedToolProgressLines OR compactionCapture. The first one's docblock forbids
// by name growing it a path parameter, because its is_capture assertion is what
// stops a hand-built payload file being swapped in behind the provenance checks. So
// this takes the shape the later ones did: its own package constants, no path
// parameter, and every provenance check written out below rather than borrowed.
// None of them can decode another's record shape, and reaching for the wrong one
// yields zero values rather than a failure.
const mcpStatusCapturePath = "../e2e/realclaude/testdata/mcp_status_v" +
	mcpStatusCaptureVersion + ".json"

// mcpStatusCaptureVersion is the claude release the capture was taken at, spliced
// into the path above rather than repeated so the filename cannot drift from the
// version this reader enforces. The producing side refuses to write under a
// mismatched name (mcapRecord.fixtureWorthy), so a claude upgrade is a loud
// instruction to re-capture and repin rather than a fixture that quietly measures
// another release.
const mcpStatusCaptureVersion = "2.1.259"

// mcpStatusPinnedServerKeys IS THE MEASUREMENT THIS TICKET COMMITS: the sorted
// union of the keys claude actually sent on the per-server objects of an
// `mcp_status` reply.
//
// It exists because the only shapes on record for that reply are DECLARATIONS.
// `sdk.d.ts` promises `{name, status, serverInfo?, error?, config?, scope?}`; the
// claude 2.1.259 binary's own bundled request schema declares two more, `tools` and
// `capabilities`. Neither is a wire measurement, and a decode arm in #2275 written
// against either drops whatever the other one knows about. A UNION OVER-REPORTS by
// construction — it names every key SOME server carried, not every key EVERY server
// carried — and the record's per-server key sets are the per-entry truth behind it.
//
// IT IS EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN, and that is what sequences
// this family. The fixture cannot exist before `make e2e-realclaude` produces it,
// which happens after verification, so a reader asserting against bytes any earlier
// would redden `make check` for every unrelated ticket. mcpStatusReaderGate turns
// that into a state machine with exactly one legal skip: filling this slice is the
// commit that lands the fixture, and a fixture landing WITHOUT it fatals rather than
// passing quietly.
//
// THE FILLING IS A SOURCE EDIT, NOT A `git add`. The dispatcher's real-claude gate
// verifies from a detached worktree it then discards and never runs `git add`, so
// the probe's in-repo fixture write is lost exactly as completely as a tempdir write
// would be — that is what happened to #2229, whose bytes reached the tree only later
// out of the run's surviving artifact directory. Whoever lands those bytes fills this
// slice from the record's own `servers[].keys` in the same commit.
var mcpStatusPinnedServerKeys = []string{}

// The four states of (fixture, pin). Only the first is a skip, and only on the leg
// before the live gate has ever run.
const (
	mcpStatusGateSkip  = "skip"
	mcpStatusGateRun   = "run"
	mcpStatusGateFatal = "fatal"
)

// mcpStatusReaderGate is pure so all four quadrants are proved on every run,
// including the leg where the fixture is still absent and the reader itself cannot
// assert anything.
func mcpStatusReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return mcpStatusGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on an " +
			"authenticated machine runs TestRealClaude_MCPStatusCapture, which arms on this fixture's " +
			"absence and writes it in-repo. This is the ONLY state in which this reader may skip, and it " +
			"ends the moment the bytes and the pin land together"
	case !fixtureExists && pinFilled:
		return mcpStatusGateFatal, "mcpStatusPinnedServerKeys names the keys a capture observed but " +
			mcpStatusCapturePath + " is gone. Restore the fixture, or if the capture was deliberately " +
			"dropped, empty the pin in the same commit — a pin with no bytes behind it is a measurement " +
			"nothing supports"
	case fixtureExists && !pinFilled:
		return mcpStatusGateFatal, "the capture at " + mcpStatusCapturePath + " has landed but " +
			"mcpStatusPinnedServerKeys is still empty, so nothing pins what it measured. Read the " +
			"record's servers[].keys and write their union here IN THE COMMIT THAT ADDS THE FIXTURE. " +
			"Until then the bytes are committed and unpinned, which is the state this gate exists to " +
			"make impossible"
	default:
		return mcpStatusGateRun, ""
	}
}

// mcpStatusCapture is the slice of the record this reader needs. payload is a JSON
// STRING holding the whole line (payload_encoding says so per frame), not a nested
// object, so the bytes can be re-decoded as claude sent them.
type mcpStatusCapture struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Requests      []struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
	} `json:"requests"`
	Frames []struct {
		Index           int    `json:"index"`
		Type            string `json:"type"`
		RequestID       string `json:"request_id"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"frames"`
	Servers []struct {
		Name string   `json:"name"`
		Keys []string `json:"keys"`
	} `json:"servers"`
}

// mcpStatusServersFrom locates the `mcpServers` array in one reply line and returns
// its entries undecoded.
//
// THREE PLACEMENTS ARE READ — top level, under `response`, and under
// `response.response` — and the third is where a real reply is known to put its
// payload. This package's own control_response arm records, as measured shape, that
// `subtype` and `request_id` arrive nested under `response` rather than at top
// level; #1688 measured the `initialize` reply nesting its payload ONE LEVEL DEEPER
// than that. A reader that stops at either of the first two levels reports a false
// absence, which is exactly what the first run of that probe did.
//
// Written out here rather than imported from the probe: that package is behind a
// build tag this one does not carry, and re-deriving is also what makes the
// comparison below a measurement rather than an agreement by construction.
func mcpStatusServersFrom(line []byte) ([]map[string]json.RawMessage, bool) {
	var env struct {
		MCPServers json.RawMessage `json:"mcpServers"`
		Response   struct {
			MCPServers json.RawMessage `json:"mcpServers"`
			Response   struct {
				MCPServers json.RawMessage `json:"mcpServers"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, false
	}
	for _, cand := range []json.RawMessage{
		env.MCPServers, env.Response.MCPServers, env.Response.Response.MCPServers,
	} {
		// PRESENCE IS DECIDED ON THE RAW BYTES, not on a decoded slice: unmarshalling
		// straight into a slice makes `"mcpServers":[]` and an absent key both arrive as
		// nil, and "claude reported no servers" is a different finding from "claude sent
		// no such key".
		if len(cand) == 0 || string(cand) == "null" {
			continue
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(cand, &entries); err != nil {
			return nil, false
		}
		return entries, true
	}
	return nil, false
}

// mcpStatusUnion returns the sorted union of the keys across a set of entries.
// Sorted because Go's map iteration is randomised and an unsorted union would
// compare differently on every run.
func mcpStatusUnion(entries []map[string]json.RawMessage) []string {
	seen := map[string]bool{}
	for _, e := range entries {
		for k := range e {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestRealClaudeMCPStatusCaptureServerKeysArePinned reads the committed capture and
// pins the key set claude sent on every server the `mcp_status` reply reported —
// AC 3 and AC 5.
//
// Every failure once the gate says run is t.Fatalf, never a skip: the capture is
// committed, so a missing or empty record is a broken premise rather than an
// unavailable resource. Zero reported servers fatals here, one level below any
// future decode arm that loops over them, so none of them can pass vacuously.
func TestRealClaudeMCPStatusCaptureServerKeysArePinned(t *testing.T) {
	raw, readErr := os.ReadFile(mcpStatusCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", mcpStatusCapturePath, readErr)
	}
	switch action, reason := mcpStatusReaderGate(exists, len(mcpStatusPinnedServerKeys) > 0); action {
	case mcpStatusGateSkip:
		t.Skipf("#2272: %s", reason)
	case mcpStatusGateFatal:
		t.Fatalf("#2272: %s", reason)
	}

	var capture mcpStatusCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", mcpStatusCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written payload; a guessed reply carries whatever keys its author expected",
			mcpStatusCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
	// leading token; an "<unavailable: ...>" version fails it too, which is correct — a
	// capture that could not read the version it was taken at cannot vouch for the
	// release its filename claims.
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != mcpStatusCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at the "+
			"pinned version, or bump mcapFixtureVersion, mcpStatusCaptureVersion and "+
			"mcpStatusPinnedServerKeys together", mcpStatusCapturePath, capture.ClaudeVersion,
			mcpStatusCaptureVersion)
	}

	// The request id the `mcp_status` verb went out under. The reply is found through
	// it rather than by scanning for a line that happens to carry an `mcpServers` key:
	// three verbs were sent on one child and any of them may answer with a server list.
	var statusID string
	for _, r := range capture.Requests {
		if r.Subtype == "mcp_status" {
			statusID = r.RequestID
			break
		}
	}
	if statusID == "" {
		t.Fatalf("%s: no recorded request carries subtype mcp_status, so nothing identifies which "+
			"reply this pin describes", mcpStatusCapturePath)
	}

	// Re-derived from each reply's OWN bytes rather than taken from the record's
	// servers[] labels. Those labels were written by the probe, and a reader that
	// trusted them would pin the probe's decoding rather than claude's line.
	var derived []map[string]json.RawMessage
	replies := 0
	for _, f := range capture.Frames {
		if f.RequestID != statusID {
			continue
		}
		replies++
		if f.PayloadEncoding != "json-string" {
			t.Fatalf("%s: frame %d payload_encoding = %q, want %q — a reply that was not valid UTF-8 "+
				"carries no readable payload, so its keys cannot be re-derived", mcpStatusCapturePath,
				f.Index, f.PayloadEncoding, "json-string")
		}
		entries, ok := mcpStatusServersFrom([]byte(f.Payload))
		if !ok {
			continue
		}
		derived = append(derived, entries...)
	}
	if replies == 0 {
		t.Fatalf("%s: no frame answers the mcp_status request id %q; the record names a request "+
			"nothing replied to and the pin below would describe an empty reading",
			mcpStatusCapturePath, statusID)
	}
	if len(derived) == 0 {
		t.Fatalf("%s: the mcp_status reply reported ZERO servers. A capture with none is vacuous — "+
			"every decode arm built on it would pass without reading a key claude sent, and #2275 reads "+
			"this file", mcpStatusCapturePath)
	}

	got := mcpStatusUnion(derived)
	want := append([]string(nil), mcpStatusPinnedServerKeys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: the reported servers carry keys %v, but mcpStatusPinnedServerKeys says %v.\n"+
			"If claude's shape genuinely changed, re-capture at the new version and repin both ends "+
			"together; a pin edited to match a fixture nobody re-read turns this measurement into an "+
			"agreement with itself", mcpStatusCapturePath, got, want)
	}

	// The record's own per-server key sets are what a later reader will loop over, and
	// they are the probe's decoding of the same bytes. Their union must be the one
	// re-derived above; a disagreement means the record describes something other than
	// the payload it carries, and the labels are then untrustworthy wholesale.
	labelled := map[string]bool{}
	for _, s := range capture.Servers {
		if len(s.Keys) == 0 {
			t.Fatalf("%s: recorded server %q carries an empty key set; the record contradicts itself "+
				"and no decode arm can be written against it", mcpStatusCapturePath, s.Name)
		}
		for _, k := range s.Keys {
			labelled[k] = true
		}
	}
	labelledKeys := make([]string, 0, len(labelled))
	for k := range labelled {
		labelledKeys = append(labelledKeys, k)
	}
	sort.Strings(labelledKeys)
	if strings.Join(labelledKeys, ",") != strings.Join(got, ",") {
		t.Fatalf("%s: the record's servers[].keys union is %v but the reply's own bytes carry %v — "+
			"the record's labels disagree with the payload they describe", mcpStatusCapturePath,
			labelledKeys, got)
	}
}

// TestMCPStatusReaderGateHasExactlyOneLegalSkip proves the four quadrants of the
// sequencing gate, and it runs on every leg including the one where the fixture
// does not exist yet — which is the leg where the pin test above can assert
// nothing and this is the only non-vacuous coverage in the file.
//
// The third row is the one the whole design turns on. #1763 spent real tokens on a
// live gate that ran green and landed none of the three artifacts its acceptance
// criteria asked for; here the equivalent miss is bytes that land with nothing
// pinning what they measured, and a gate that passed in that state would hide it
// exactly as well.
func TestMCPStatusReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		fixtureExists bool
		pinFilled     bool
		want          string
	}{
		{"before the live gate has run: the one legal skip", false, false, mcpStatusGateSkip},
		{"a pin whose bytes are gone", false, true, mcpStatusGateFatal},
		{"bytes committed with nothing pinning them", true, false, mcpStatusGateFatal},
		{"the steady state", true, true, mcpStatusGateRun},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := mcpStatusReaderGate(tc.fixtureExists, tc.pinFilled)
			if action != tc.want {
				t.Errorf("mcpStatusReaderGate(%v, %v) = %q, want %q", tc.fixtureExists, tc.pinFilled,
					action, tc.want)
			}
			if action == mcpStatusGateRun && reason != "" {
				t.Errorf("the run state named a reason %q; nothing reads it", reason)
			}
			if action != mcpStatusGateRun && reason == "" {
				t.Errorf("%s named no reason; the skip or fatal message would say nothing", action)
			}
		})
	}
}

// TestMCPStatusServersFromReadsAllThreePlacements is the guard on the one decoding
// choice this reader makes that could silently report a false absence.
//
// The `response.response` row is the load-bearing one: it is where a real
// `initialize` reply was measured to put its payload, one level deeper than this
// package's control_response arm records for `subtype`/`request_id`. A reader that
// stopped at `response` would report "claude sent no mcpServers" on a reply that
// carried one, and the pin above would then be an empty measurement that still
// passed its own assertions.
func TestMCPStatusServersFromReadsAllThreePlacements(t *testing.T) {
	t.Parallel()
	const entry = `{"name":"pyry_approve","status":"connected"}`
	tests := []struct {
		name  string
		line  string
		want  int
		found bool
	}{
		{"top level", `{"mcpServers":[` + entry + `]}`, 1, true},
		{"under response", `{"response":{"mcpServers":[` + entry + `]}}`, 1, true},
		{
			name: "under response.response, where a measured reply puts its payload",
			line: `{"response":{"subtype":"success","response":{"mcpServers":[` + entry + `]}}}`,
			want: 1, found: true,
		},
		{
			// An empty array is a REPORT of no servers and must read as found: the pin
			// test's zero-server fatal is what handles it, and a reader collapsing this
			// into "absent" would send it down the wrong diagnosis.
			name: "an empty array is found, not absent",
			line: `{"response":{"response":{"mcpServers":[]}}}`,
			want: 0, found: true,
		},
		{"a reply carrying no such key at all", `{"response":{"subtype":"success"}}`, 0, false},
		{"an explicit null is not a report", `{"response":{"mcpServers":null}}`, 0, false},
		{"an undecodable line reports absence rather than panicking", `{"response":`, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries, ok := mcpStatusServersFrom([]byte(tc.line))
			if ok != tc.found {
				t.Errorf("mcpStatusServersFrom() found = %v, want %v", ok, tc.found)
			}
			if len(entries) != tc.want {
				t.Errorf("mcpStatusServersFrom() returned %d entries, want %d", len(entries), tc.want)
			}
		})
	}
}
