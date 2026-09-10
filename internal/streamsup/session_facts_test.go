package streamsup

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// #2252 — the system/init line's claude_code_version and permissionMode reach a
// client as turnevent.SessionFacts.
//
// TWO COMMITTED CAPTURES DRIVE THIS FILE, and needing both is the point rather
// than belt-and-braces. The 2.1.259 capture (#2251's, three init lines) is the one
// the acceptance criteria name; every one of its lines reads permissionMode
// "default", which is exactly the value a producer that hardcoded, lowercased or
// allow-listed the mode would also produce. The 2.1.220 capture reads
// "bypassPermissions", so it is what makes the mode assertion a statement about
// mapping rather than a coincidence.

// sessionFactsInitLineFixture builds a system/init line from the TWO mapped keys
// and no `model`, so the line produces exactly one event and an event count is an
// unambiguous statement about this variant.
//
// It invents no field structure — both keys are the captures' own, in claude's own
// spelling, camelCase permissionMode included. Absent is a DIFFERENT input from
// present-but-empty and is written as a raw line at its call site rather than bent
// into this helper, for modelInitLineFixture's stated reason: both are asserted, so
// neither may be expressible only as the other.
func sessionFactsInitLineFixture(t *testing.T, version, mode string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":                "system",
		"subtype":             "init",
		"claude_code_version": version,
		"permissionMode":      mode,
	})
	if err != nil {
		t.Fatalf("marshalling system/init fixture: %v", err)
	}
	return string(b)
}

// sessionFactsEvent drives one line through the shipped parser and returns the
// single SessionFacts it must emit.
//
// It FILTERS rather than requiring a count of one, which is the difference from
// modelAnnouncedEvent and is forced by the shape of the change: one init line now
// produces two variants, so a line carrying a model legitimately yields a
// ModelAnnounced alongside. The count that matters here — exactly one SessionFacts
// — is what the filter asserts.
func sessionFactsEvent(t *testing.T, line string) turnevent.SessionFacts {
	t.Helper()
	got := collectEvents(line)
	var found []turnevent.SessionFacts
	for _, ev := range got {
		if sf, ok := ev.(turnevent.SessionFacts); ok {
			found = append(found, sf)
		}
	}
	if len(found) != 1 {
		t.Fatalf("SessionFacts count: got %d, want 1 (all events: %#v)", len(found), got)
	}
	return found[0]
}

// effortCaptureInitLines returns the raw bytes of every system/init line in the
// capture #2251 committed, in the order they appear.
//
// It reads the SAME file effortInitPins measures and reuses that reader's record
// type, so the two cannot drift onto different bytes. The gate is consulted for
// effortInitReaderGate's stated reason: on the leg before the live capture has ever
// run there are no bytes to assert against, and that is the one state in which a
// reader here may skip.
//
// Each StdoutEvents entry is the line's OWN bytes rather than a recorder's
// rendering of it, which is what makes driving them through the parser a replay.
// They are COMPACTED before being returned, and that is a necessity rather than a
// tidy-up: the committed record is written indented, so an entry spans many physical
// lines while claude wrote it as one. Feeding it uncompacted to a line-oriented
// parser yields one Unrecognized per brace. json.Compact only removes insignificant
// whitespace — key order and every value survive byte-for-byte — so the replay stays
// a replay.
func effortCaptureInitLines(t *testing.T) [][]byte {
	t.Helper()

	raw, err := os.ReadFile(effortInitCapturePath)
	if err != nil {
		if action, reason := effortInitReaderGate(false, len(effortInitPins) > 0); action == effortInitGateSkip {
			t.Skipf("#2252: %s", reason)
		}
		t.Fatalf("reading capture %s: %v", effortInitCapturePath, err)
	}

	var rec effortInitRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decoding capture %s: %v", effortInitCapturePath, err)
	}

	var lines [][]byte
	for _, ev := range rec.StdoutEvents {
		var envelope struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal(ev, &envelope); err != nil {
			continue
		}
		if envelope.Type == "system" && envelope.Subtype == "init" {
			var compact bytes.Buffer
			if err := json.Compact(&compact, ev); err != nil {
				t.Fatalf("compacting a captured init line: %v", err)
			}
			lines = append(lines, compact.Bytes())
		}
	}
	if len(lines) == 0 {
		t.Fatalf("%s: no system/init line among %d recorded events", effortInitCapturePath, len(rec.StdoutEvents))
	}
	return lines
}

// TestParser_SessionFactsMapsFromTheEffortCapture is AC1's central assertion: each
// system/init line of the capture the blocker committed becomes one
// turnevent.SessionFacts carrying claude_code_version and permissionMode, and
// NOTHING ELSE from the line.
//
// Both values are DERIVED from the line's own payload rather than pinned, which is
// this family's rule; effortInitPins is where the literal values are pinned, and
// pinning them here as well would be a second copy of a measurement that already has
// a machine-enforced home.
//
// The whole-event reflect.DeepEqual is what carries "and nothing else from the
// line", chosen for TestParser_ModelAnnouncedMapsFromCapture's stated reason: a later
// field that started carrying cwd, session_id, memory_paths or messaging_socket_path
// is non-zero, and a literal leaving it zero fails. It also pins TruncatedFields as
// nil, which no string sweep would visit.
func TestParser_SessionFactsMapsFromTheEffortCapture(t *testing.T) {
	t.Parallel()
	lines := effortCaptureInitLines(t)

	for i, line := range lines {
		var payload map[string]any
		if err := json.Unmarshal(line, &payload); err != nil {
			t.Fatalf("init line %d: decoding the captured payload: %v", i, err)
		}
		wantVersion, _ := payload["claude_code_version"].(string)
		wantMode, _ := payload["permissionMode"].(string)
		if wantVersion == "" || wantMode == "" {
			t.Fatalf("init line %d: the capture carries no string claude_code_version/permissionMode, "+
				"so the routing of neither field can be proven against it", i)
		}
		// The four keys whose absence matters most (see turnevent.SessionFacts's doc),
		// checked to be PRESENT on the line so the DeepEqual below is a real statement
		// about dropping them rather than a statement about a line that never carried
		// them. memory_paths and messaging_socket_path are the two the 2.1.259 capture
		// added over the 22-key census the docs used to carry.
		for _, key := range []string{"cwd", "session_id", "memory_paths", "messaging_socket_path"} {
			if _, ok := payload[key]; !ok {
				t.Fatalf("init line %d: the capture carries no %q, so the drop assertion would be vacuous", i, key)
			}
		}

		ev := sessionFactsEvent(t, string(line))
		want := turnevent.SessionFacts{ClaudeCodeVersion: wantVersion, PermissionMode: wantMode}
		if !reflect.DeepEqual(ev, want) {
			t.Errorf("init line %d event: got %#v, want %#v — the two values verbatim and nothing "+
				"else from the line", i, ev, want)
		}
	}
}

// TestParser_SessionFactsMapsTheDropcapPermissionMode is the assertion the capture
// above CANNOT make. Every one of its three init lines reads permissionMode
// "default", so a producer that hardcoded that value, or lowercased, or checked the
// mode against an allow-list and substituted a fallback, maps all three to
// themselves and stays green.
//
// The 2.1.220 capture reads bypassPermissions — an ESCALATED posture, and precisely
// the one an operator is checking for when they read this frame — so the two
// captures together prove the mapping carries what the line says rather than what
// the producer expects.
func TestParser_SessionFactsMapsTheDropcapPermissionMode(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "init")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	wantVersion, _ := payload["claude_code_version"].(string)
	wantMode, _ := payload["permissionMode"].(string)
	if wantVersion == "" || wantMode == "" {
		t.Fatalf("the capture carries no string claude_code_version/permissionMode")
	}

	ev := sessionFactsEvent(t, string(line))
	want := turnevent.SessionFacts{ClaudeCodeVersion: wantVersion, PermissionMode: wantMode}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("event: got %#v, want %#v", ev, want)
	}
	// The canary: proves this capture is the one carrying a mode the other does NOT,
	// so the pair is doing the work claimed for it. It is a real value — the dropcap
	// redactor rewrites session_id, cwd and the FIFO path, and touches neither of
	// these keys.
	if ev.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode: got %q, want the dropcap capture's observed %q — without a second "+
			"observed mode this file proves only that \"default\" round-trips", ev.PermissionMode, "bypassPermissions")
	}
}

// TestParser_SessionFactsCarriesTheValuesVerbatim is the half of AC1 neither
// capture can prove. Between them they show 2.1.220, 2.1.259, default and
// bypassPermissions — all already lowercase, all already well formed — so a mapping
// that lowercased, normalised a version, or rewrote an unknown mode to a fallback
// would map every captured value to itself and both tests above would stay green.
// These rows are synthesized for exactly that reason, and they invent no field
// structure: the only keys are the captures' own.
func TestParser_SessionFactsCarriesTheValuesVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
		mode    string
		why     string
	}{
		{
			name: "a mode in no published list survives", version: "2.1.259", mode: "someModeClaudeHasNotShippedYet",
			why: "the daemon does not control what claude runs under and may not reject it; an " +
				"allow-list would drop the first report of a new posture, which is the case an " +
				"operator most needs to see",
		},
		{
			name: "mixed case is NOT folded", version: "2.1.259-RC1", mode: "BypassPermissions",
			why: "the one transform both captures' already-lowercase values could hide",
		},
		{
			name: "a version that does not parse as a triple survives", version: "2026.09-nightly+deadbeef",
			mode: "default",
			why: "a producer that parsed a version would have to invent an answer for the first " +
				"build carrying a suffix; carrying the string has no such question to get wrong",
		},
		{
			name: "surrounding whitespace is NOT trimmed", version: " 2.1.259 ", mode: " default ",
			why: "trimming is a repair, and a value that arrives padded is a fact about what claude sent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev := sessionFactsEvent(t, sessionFactsInitLineFixture(t, tt.version, tt.mode))
			if ev.ClaudeCodeVersion != tt.version {
				t.Errorf("ClaudeCodeVersion: got %q, want claude's value %q verbatim (%s)",
					ev.ClaudeCodeVersion, tt.version, tt.why)
			}
			if ev.PermissionMode != tt.mode {
				t.Errorf("PermissionMode: got %q, want claude's value %q verbatim (%s)",
					ev.PermissionMode, tt.mode, tt.why)
			}
			if ev.TruncatedFields != nil {
				t.Errorf("TruncatedFields: got %v, want nil — every value here is far under its cap",
					ev.TruncatedFields)
			}
		})
	}
}

// TestParser_SessionFactsGate pins AC3. Both facts absent or empty produces NO
// event; at least one present produces exactly one, with the other empty.
//
// The gate DIVERGES from emitModelAnnounced's, and the divergence is what these rows
// state. There the model IS the whole payload, so an event naming none asserts
// something the line did not say. Here two facts share one event: one present fact
// is still news and the other's absence is claude's own choice, which is
// emitBackgroundTaskStarted's rule. Both-empty is the only case that asserts nothing.
//
// Every row's line omits `model`, so the event count is a statement about this
// variant alone.
func TestParser_SessionFactsGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		line        string
		wantEvent   bool
		wantVersion string
		wantMode    string
		why         string
	}{
		{
			name: "both keys absent", line: `{"type":"system","subtype":"init","session_id":"s"}`,
			wantEvent: false,
			why: "a line carrying neither fact reports nothing, and an event with two empty " +
				"strings would assert that claude described itself while naming nothing",
		},
		{
			name: "both present but empty", line: sessionFactsInitLineFixture(t, "", ""),
			wantEvent: false,
			why:       "answered identically to absence, which is what makes plain string fields sufficient",
		},
		{
			name: "version absent, mode present", line: `{"type":"system","subtype":"init","permissionMode":"plan"}`,
			wantEvent: true, wantMode: "plan",
			why: "one present fact is news; the absent one is claude's choice and lands empty",
		},
		{
			name: "version present, mode absent", line: `{"type":"system","subtype":"init","claude_code_version":"2.1.259"}`,
			wantEvent: true, wantVersion: "2.1.259",
			why: "the mirror of the row above, so neither field carries the gate alone",
		},
		{
			name: "version empty, mode present", line: sessionFactsInitLineFixture(t, "", "bypassPermissions"),
			wantEvent: true, wantMode: "bypassPermissions",
			why: "present-but-empty is answered as absent PER FIELD, not per line — the gate reads " +
				"both together and only both-empty suppresses",
		},
		{
			name: "version present, mode empty", line: sessionFactsInitLineFixture(t, "2.1.220", ""),
			wantEvent: true, wantVersion: "2.1.220",
			why: "the mirror of the row above",
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

			want := 0
			if tt.wantEvent {
				want = 1
			}
			if len(events) != want {
				t.Fatalf("event count: got %d, want %d (%s) — %#v", len(events), want, tt.why, events)
			}
			// The line is CONSUMED on every path, so it never falls through to
			// consumeLine's generic drop, and the suppressed rows are SILENT: init fires
			// once per turn, so a Debug on a routine drop is the per-turn noise row this
			// family exists to avoid.
			if generic := rec.withMessage(genericDropMsgFixture); len(generic) != 0 {
				t.Errorf("the line reached consumeLine's generic drop (%d record(s)): %+v", len(generic), generic)
			}
			if all := rec.all(); len(all) != 0 {
				t.Errorf("records: got %d, want 0 — every row here decodes, so no arm may speak: %+v", len(all), all)
			}
			if !tt.wantEvent {
				return
			}
			sf, ok := events[0].(turnevent.SessionFacts)
			if !ok {
				t.Fatalf("event type: got %T, want turnevent.SessionFacts", events[0])
			}
			if sf.ClaudeCodeVersion != tt.wantVersion {
				t.Errorf("ClaudeCodeVersion: got %q, want %q (%s)", sf.ClaudeCodeVersion, tt.wantVersion, tt.why)
			}
			if sf.PermissionMode != tt.wantMode {
				t.Errorf("PermissionMode: got %q, want %q (%s)", sf.PermissionMode, tt.wantMode, tt.why)
			}
		})
	}
}

// TestParser_SessionFactsFieldCaps pins AC2's construction-time bound. The cut is
// applied before the event reaches the sink, so an oversized value never enters the
// event stream, a queue, or a log — the ordering maxModelField's cap has, and the
// only thing that makes a bound real rather than cosmetic.
//
// The two constants are read through their own names rather than a shared literal:
// they are separate constants for maxRateLimitField's stated reason, and a test that
// assumed one number would keep passing after someone moved one of them.
func TestParser_SessionFactsFieldCaps(t *testing.T) {
	t.Parallel()

	t.Run("each field is cut at its own cap and names itself", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name    string
			version string
			mode    string
			want    []string
		}{
			{
				name: "neither over", version: strings.Repeat("v", maxClaudeVersionField),
				mode: strings.Repeat("p", maxPermissionModeField), want: nil,
			},
			{
				name: "version over", version: strings.Repeat("v", maxClaudeVersionField+1),
				mode: "default", want: []string{"claude_code_version"},
			},
			{
				name: "mode over", version: "2.1.259",
				mode: strings.Repeat("p", maxPermissionModeField+1), want: []string{"permission_mode"},
			},
			{
				// The ORDER is the declaration order, and it is asserted rather than left
				// to the reader: TruncatedFields is built by sequential bound() calls
				// precisely so the order rests on something visible.
				name: "both over", version: strings.Repeat("v", maxClaudeVersionField+9),
				mode: strings.Repeat("p", maxPermissionModeField+9),
				want: []string{"claude_code_version", "permission_mode"},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				ev := sessionFactsEvent(t, sessionFactsInitLineFixture(t, tt.version, tt.mode))
				if len(ev.ClaudeCodeVersion) > maxClaudeVersionField {
					t.Errorf("ClaudeCodeVersion is %d bytes, over the %d-byte cap",
						len(ev.ClaudeCodeVersion), maxClaudeVersionField)
				}
				if len(ev.PermissionMode) > maxPermissionModeField {
					t.Errorf("PermissionMode is %d bytes, over the %d-byte cap",
						len(ev.PermissionMode), maxPermissionModeField)
				}
				if !slices.Equal(ev.TruncatedFields, tt.want) {
					t.Errorf("TruncatedFields: got %v, want %v — the DAEMON's names, in declaration "+
						"order", ev.TruncatedFields, tt.want)
				}
			})
		}
	})

	t.Run("a cut landing mid-rune leaves valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// One byte short of the cap, then a three-byte rune: the cut lands inside it,
		// and truncateField deletes the partial rather than emitting a replacement.
		ev := sessionFactsEvent(t, sessionFactsInitLineFixture(t,
			strings.Repeat("v", maxClaudeVersionField-1)+"€", "default"))
		if !utf8.ValidString(ev.ClaudeCodeVersion) {
			t.Errorf("ClaudeCodeVersion is not valid UTF-8 after the cut: %q", ev.ClaudeCodeVersion)
		}
		// Short of the cap, not at it — and not more than a rune short.
		if len(ev.ClaudeCodeVersion) > maxClaudeVersionField || len(ev.ClaudeCodeVersion) < maxClaudeVersionField-3 {
			t.Errorf("len(ClaudeCodeVersion): got %d, want within 3 bytes below %d",
				len(ev.ClaudeCodeVersion), maxClaudeVersionField)
		}
		if !slices.Contains(ev.TruncatedFields, "claude_code_version") {
			t.Errorf("TruncatedFields: got %v, want it to name claude_code_version", ev.TruncatedFields)
		}
	})
}

// TestParser_SystemInitLineDeclaresExactlyThreeKeys is AC4, and it is the assertion
// that makes every "this variant carries nothing else from the line" claim in this
// file structural rather than argued. A field never declared on the decode target
// cannot reach an event or a log by any path, whatever a later sweep forgets to
// check — so a widening has to be deliberate enough to edit this table.
//
// The four named absences are the ones that matter: cwd and the two path keys are
// the operator's local filesystem, and session_id is claude's session identity and
// NOT the daemon's conversation identity (#1380).
func TestParser_SystemInitLineDeclaresExactlyThreeKeys(t *testing.T) {
	t.Parallel()

	want := []string{"model", "claude_code_version", "permissionMode"}

	rt := reflect.TypeOf(systemInitLine{})
	var got []string
	for i := range rt.NumField() {
		tag, ok := rt.Field(i).Tag.Lookup("json")
		if !ok {
			t.Fatalf("field %q carries no json tag; a decode target's key set is its tags", rt.Field(i).Name)
		}
		name, _, _ := strings.Cut(tag, ",")
		got = append(got, name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("systemInitLine declares %v, want exactly %v — adding a key here widens what the "+
			"daemon retains from claude's init line, and the doc's argument for the omissions has to "+
			"be re-made rather than inherited", got, want)
	}
	for _, absent := range []string{"cwd", "session_id", "memory_paths", "messaging_socket_path"} {
		if slices.Contains(got, absent) {
			t.Errorf("systemInitLine declares %q; it is the operator's filesystem or claude's own "+
				"session identity, and a field never declared cannot leak", absent)
		}
	}
}

// TestParser_InitLineEmitsBothVariantsInOrder pins the seam #2252 changed: ONE
// system/init line now produces TWO events from ONE decode.
//
// The ORDER is asserted because it is observable — these reach a client in the order
// the parser emits them — and because a reader of the arm should find the order
// stated somewhere that fails when it moves.
//
// The undecodable row is the other half, and it is the reason the decode was hoisted
// out of emitModelAnnounced rather than duplicated: one malformed line must produce
// ONE record, not one per variant.
func TestParser_InitLineEmitsBothVariantsInOrder(t *testing.T) {
	t.Parallel()

	t.Run("a line carrying all three facts", func(t *testing.T) {
		t.Parallel()
		line := `{"type":"system","subtype":"init","model":"claude-sonnet-5",` +
			`"claude_code_version":"2.1.259","permissionMode":"default"}`
		got := collectEvents(line)
		if len(got) != 2 {
			t.Fatalf("event count: got %d, want 2 — %#v", len(got), got)
		}
		if _, ok := got[0].(turnevent.ModelAnnounced); !ok {
			t.Errorf("event 0: got %T, want turnevent.ModelAnnounced first", got[0])
		}
		if _, ok := got[1].(turnevent.SessionFacts); !ok {
			t.Errorf("event 1: got %T, want turnevent.SessionFacts second", got[1])
		}
	})

	t.Run("an undecodable line produces one record and no events", func(t *testing.T) {
		t.Parallel()
		// A non-string permissionMode fails the WHOLE-line decode, which is the
		// rung-disturbance the widened target accepts and systemInitLine's doc records:
		// this line's model would have emitted before #2252.
		const undecodableDigits = "225222522252"
		line := `{"type":"system","subtype":"init","model":"claude-sonnet-5","permissionMode":` +
			undecodableDigits + `}`

		rec := &logRecorder{}
		var events []turnevent.Event
		p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}

		if len(events) != 0 {
			t.Errorf("event count: got %d, want 0 — %#v", len(events), events)
		}
		if generic := rec.withMessage(genericDropMsgFixture); len(generic) != 0 {
			t.Errorf("the line reached consumeLine's generic drop (%d record(s)): %+v", len(generic), generic)
		}
		all := rec.all()
		drops := rec.withMessage(undecodableSystemLineMsgFixture)
		if len(drops) != 1 {
			t.Fatalf("records with message %q: got %d, want exactly 1 — the decode is shared by both "+
				"variants precisely so one bad line is one record (all records: %+v)",
				undecodableSystemLineMsgFixture, len(drops), all)
		}
		if !reflect.DeepEqual(drops[0].attrs, map[string]string{"subtype": "init"}) {
			t.Errorf("drop attrs: got %v, want exactly {subtype: init} — an `err` attr here would "+
				"carry encoding/json's quoted copy of the input", drops[0].attrs)
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

// TestParser_SessionFactsIsLoggedContentFree is the assertion the per-path attr
// checks do NOT cover: an implementation that gets every attr right AND also logs
// "permission_mode", il.PermissionMode on the emit path passes every one of them.
//
// It sweeps every record's message and every attribute value on the EMIT path as
// well as both drop paths. The constraint is #833's posture, restated across
// internal/relay's v2session_settings.go and internal/sessions' pool.go as "model /
// effort / YOLO values are NEVER logged at any level". A permission mode is the
// sharpest case in that family after the model itself: it names the posture the
// child is running under, and it is exactly the field a log line explaining an
// unexpected posture would reach for.
//
// The sweep covers the captured line's cwd and session_id as well, so the omission
// is proved END TO END rather than only at the struct.
func TestParser_SessionFactsIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	const (
		emitVersion       = "version-sentinel-225201"
		emitMode          = "mode-sentinel-225202"
		undecodableSuffix = "225203225203"
	)
	captured := capturedSystemLine(t, "init")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedVersion, _ := payload["claude_code_version"].(string)
	capturedMode, _ := payload["permissionMode"].(string)
	capturedCwd, _ := payload["cwd"].(string)
	capturedSession, _ := payload["session_id"].(string)
	if capturedVersion == "" || capturedMode == "" || capturedCwd == "" || capturedSession == "" {
		t.Fatalf("the capture carries no string claude_code_version/permissionMode/cwd/session_id, " +
			"so this sweep would be vacuous")
	}

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	lines := []string{
		// The emit path, twice: the synthesized sentinels, then the real captured line
		// with its cwd and session_id intact.
		sessionFactsInitLineFixture(t, emitVersion, emitMode),
		string(captured),
		// The suppressed path and the undecodable path.
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"system","subtype":"init","permissionMode":` + undecodableSuffix + `}`,
	}
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}

	// The sentinel line carries no model, the captured one does: one SessionFacts each
	// plus the capture's own ModelAnnounced.
	if len(events) != 3 {
		t.Fatalf("event count: got %d, want 3 — %#v", len(events), events)
	}
	// ONE record and not two or three: both emit paths log nothing, the suppressed
	// line logs nothing, and only the undecodable line speaks.
	all := rec.all()
	if len(all) != 1 {
		t.Fatalf("records: got %d, want exactly 1 (the undecodable line): %+v", len(all), all)
	}
	for _, needle := range []string{emitVersion, emitMode, capturedVersion, capturedMode, capturedCwd, capturedSession, undecodableSuffix} {
		for _, r := range all {
			if strings.Contains(r.msg, needle) {
				t.Errorf("record message carries %q: %q", needle, r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, needle) {
					t.Errorf("record %q attr %q carries %q", r.msg, k, needle)
				}
			}
		}
	}
}
