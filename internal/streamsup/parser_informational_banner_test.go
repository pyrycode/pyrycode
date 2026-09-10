package streamsup

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The lines in this file are HAND-BUILT, on parser_permission_denied_test.go's
// division of labour. claude's own bytes are replayed in
// informational_capture_test.go, which asserts what the arm does with the one
// informational line on record; nothing here may assert on a shape this file
// invented. What these rows exercise is what the capture does not contain — an
// over-cap value, an empty content, a prevent_continuation of the wrong JSON type
// — so they are necessarily authored, and the capture is what keeps them honest
// about the shape they are authored in.

// informationalLineFixture builds one system/informational line carrying the given
// claude keys. The two envelope keys are set here so no row can misspell them and
// pass by falling into a different arm.
func informationalLineFixture(t *testing.T, fields map[string]any) string {
	t.Helper()
	line := map[string]any{"type": "system", "subtype": "informational"}
	for k, v := range fields {
		line[k] = v
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling informational line fixture: %v", err)
	}
	return string(raw)
}

// oneBanner feeds a line to a fresh parser and returns the single
// turnevent.Banner it emitted, failing on any other count. "Exactly one" is
// asserted rather than "at least one" so a future arm emitting a second event for
// the same line shows up here.
func oneBanner(t *testing.T, line string) turnevent.Banner {
	t.Helper()
	events := collectEvents(line)
	if len(events) != 1 {
		t.Fatalf("collectEvents(%s) returned %d events, want exactly 1", line, len(events))
	}
	banner, ok := events[0].(turnevent.Banner)
	if !ok {
		t.Fatalf("event is %T, want turnevent.Banner", events[0])
	}
	return banner
}

// TestParser_InformationalMapsToBanner is the mapping itself: the three keys the
// captured line carries reach the three fields the variant publishes, under the
// daemon's own names.
//
// The renames are half the point of the row. `content` becomes Text and
// `prevent_continuation` becomes StopsTurn, while `level` keeps claude's spelling
// — so a row asserting only that "some value crossed" would pass on an arm that
// wired content into Level.
func TestParser_InformationalMapsToBanner(t *testing.T) {
	t.Parallel()

	line := informationalLineFixture(t, map[string]any{
		"content":              "UserPromptSubmit operation blocked by hook",
		"level":                "warning",
		"prevent_continuation": true,
	})
	got := oneBanner(t, line)
	want := turnevent.Banner{
		Level:     "warning",
		Text:      "UserPromptSubmit operation blocked by hook",
		StopsTurn: true,
	}
	if got != want {
		t.Errorf("banner = %+v, want %+v", got, want)
	}
}

// TestParser_InformationalGatesOnEmptyContent pins the arm's ONE gate inside
// `make check`, which is why it is here rather than left to the build-tagged
// classification row.
//
// The gate is emitCompactionBoundary's answer, not emitPermissionDenied's, and the
// two rows below are what make that choice falsifiable. A line with text is news
// whatever else it carries; a line with none is a first-class notice a client would
// render as empty chrome, and neither a level nor a stops-turn flag is something a
// consumer can act on alone. Removing the gate reddens the first row; widening it to
// cover a missing level reddens the second.
func TestParser_InformationalGatesOnEmptyContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fields    map[string]any
		wantEvent bool
		why       string
	}{
		{
			name:      "no content key at all",
			fields:    map[string]any{"level": "warning", "prevent_continuation": true},
			wantEvent: false,
			why:       "the field the variant exists to carry is absent, so the frame would be a notice saying nothing",
		},
		{
			name:      "content present but empty",
			fields:    map[string]any{"content": "", "level": "info"},
			wantEvent: false,
			why:       "an empty string is the same absence spelled differently; the gate must not read presence of the key",
		},
		{
			name:      "text with nothing else",
			fields:    map[string]any{"content": "a hook said something"},
			wantEvent: true,
			why: "the gate is on content ALONE — a missing level is an open set's absence, which " +
				"turnevent.Banner.Level says a consumer already handles, and a missing flag is false",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := collectEvents(informationalLineFixture(t, tc.fields))
			if tc.wantEvent {
				if len(events) != 1 {
					t.Fatalf("got %d events, want 1 — %s", len(events), tc.why)
				}
				return
			}
			if len(events) != 0 {
				t.Fatalf("got %d events (%T…), want 0 — %s", len(events), events[0], tc.why)
			}
		})
	}
}

// TestParser_InformationalUndecodableIsConsumedSilently drives a line whose
// prevent_continuation is not a bool. Every declared field is a concrete Go type,
// so a wrong JSON type fails the WHOLE decode — fail-closed, emitPermissionDenied's
// posture — and the line is still consumed.
//
// "Consumed" is the half worth asserting rather than assuming: `system` is on
// ignoredLineTypes, so a subtype the switch declines falls to a silent debug drop
// and never reaches emitUnrecognized. A zero-event assertion alone therefore cannot
// tell a consumed line from a declined one, and this test asserts what it can — that
// nothing reaches a client either way, and in particular no Unrecognized does.
func TestParser_InformationalUndecodableIsConsumedSilently(t *testing.T) {
	t.Parallel()

	line := `{"type":"system","subtype":"informational","content":"text","prevent_continuation":"yes"}`
	events := collectEvents(line)
	if len(events) != 0 {
		t.Fatalf("got %d events (%T…), want 0: a line that fails the decode publishes nothing",
			len(events), events[0])
	}
}

// TestParser_InformationalBoundsClaudesTwoStrings drives an over-cap value through
// both claude-derived strings and asserts what the bound did. Each row states the
// field's OVERFLOW ANSWER as well as its cap, because the two answers are the
// substance of this arm and a row that only checked a length would pass on either.
//
// The at-cap rows are what stop the table passing on an off-by-one bound: a value of
// exactly the cap must survive whole with Truncated false, so a `>=` where the arm
// has `>` reddens here rather than silently emptying a valid level in production.
//
// The asymmetry between the two answers is the trap this table exists for. Text is
// CUT and the cut is REPORTED; Level is DROPPED and the drop is NOT — so the
// over-cap level row requires Truncated to stay false, which is the assertion that
// fails if someone folds the level into truncateField for symmetry.
func TestParser_InformationalBoundsClaudesTwoStrings(t *testing.T) {
	t.Parallel()

	overText := strings.Repeat("p", maxBannerText+1)
	atText := strings.Repeat("p", maxBannerText)
	overLevel := strings.Repeat("l", maxBannerLevel+1)
	atLevel := strings.Repeat("l", maxBannerLevel)

	tests := []struct {
		name   string
		fields map[string]any
		want   turnevent.Banner
		why    string
	}{
		{
			name:   "text over cap is cut, and the cut is reported",
			fields: map[string]any{"content": overText, "level": "warning"},
			want:   turnevent.Banner{Level: "warning", Text: atText, Truncated: true},
			why:    "prose survives a cut as what it is, so it is shortened rather than emptied",
		},
		{
			name:   "text at exactly the cap is untouched",
			fields: map[string]any{"content": atText, "level": "warning"},
			want:   turnevent.Banner{Level: "warning", Text: atText},
			why:    "an off-by-one bound would cut a value that fits",
		},
		{
			name:   "level over cap is dropped, and the drop is NOT reported",
			fields: map[string]any{"content": "text", "level": overLevel},
			want:   turnevent.Banner{Text: "text"},
			why: "a client MATCHES this token, so a cut one would match nothing while still " +
				"looking like a value; the emptied scalar is directly observable and Truncated " +
				"speaks for Text alone",
		},
		{
			name:   "level at exactly the cap survives whole",
			fields: map[string]any{"content": "text", "level": atLevel},
			want:   turnevent.Banner{Level: atLevel, Text: "text"},
			why:    "an off-by-one bound would empty a level that fits",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := oneBanner(t, informationalLineFixture(t, tc.fields))
			if got != tc.want {
				t.Errorf("banner = %+v, want %+v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestParser_InformationalDropsClaudesIdentityKeys drives the two keys the captured
// line carries that the daemon does not map, and asserts they reach nothing.
//
// The guarantee is STRUCTURAL — systemInformationalLine never declares them, so
// encoding/json discards them before any daemon value exists — and this test is the
// witness rather than the mechanism. It sweeps the marshalled event for the values
// instead of naming fields, so a field added to the variant later that happened to
// carry either one would redden here too.
//
// Non-vacuity is what the distinct sentinels buy: each is a string nothing else on
// the line spells, so a sweep finding neither is a fact about the mapping rather
// than about two values that could not have appeared anyway.
func TestParser_InformationalDropsClaudesIdentityKeys(t *testing.T) {
	t.Parallel()

	const (
		sessionSentinel = "SESSION-IDENTITY-MUST-NOT-CROSS"
		uuidSentinel    = "LINE-UUID-MUST-NOT-CROSS"
	)
	got := oneBanner(t, informationalLineFixture(t, map[string]any{
		"content":    "a hook said something",
		"level":      "warning",
		"session_id": sessionSentinel,
		"uuid":       uuidSentinel,
	}))
	rendered, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the mapped event: %v", err)
	}
	for _, sentinel := range []string{sessionSentinel, uuidSentinel} {
		if strings.Contains(string(rendered), sentinel) {
			t.Errorf("the mapped banner carries %q; claude's session identity is not the daemon's "+
				"conversation identity and nothing in the daemon reads the line's uuid, so neither "+
				"may be declared on the decode target", sentinel)
		}
	}
}
