package main

// #1692 — the fake's answer to an `initialize` control_request: the canned model
// list the hermetic tier needs so #1849's publishing path and #1683's command list
// can be proven with no credentials, no tokens and no network.
//
// Untagged on purpose, exactly like stream_detect_test.go: `make check` runs this
// file in both the `test` and the `e2e` target. Reading the committed capture needs
// no build tag either — internal/e2e/realclaude is behind e2e_realclaude, but its
// testdata file is just a file, read here by path rather than through the package.
//
// READ-ONLY against testdata/. `go test` runs in the package source directory, so a
// relative path from here reaches the committed captures themselves — the hazard
// initialize_control_names_test.go bans over its own file. Nothing below opens one
// for writing, creates a file beside them, or removes one.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// initControlCaptureGlob names the committed `initialize` control-request captures
// the canned entries are cross-checked against. A hard-coded literal — no decoded
// value reaches it, so there is no path to guard.
//
// It matches MORE THAN ONE file once #1713/#1715 land their per-arm captures
// (initControlArmFixtureName mints initialize_control_v<slug>_<arm>.json, which this
// pattern covers), and one of those arms is control_no_request, whose whole content
// is that no control request was sent — so it carries no control_responses and
// contributes no entries BY DESIGN. Every non-vacuity gate below is therefore an
// AGGREGATE over the matched set, never a per-file one.
const initControlCaptureGlob = "../../realclaude/testdata/initialize_control_v*.json"

// initializeControlRequestLine hand-mirrors the inbound `initialize` control_request
// the daemon writes to the child's already-held-open stdin (#1689). Hand-written and
// not imported, same discipline as interruptControlRequestLine: streamsup's envelope
// types are unexported and the fake stays zero-dependency.
func initializeControlRequestLine(requestID string) string {
	return fmt.Sprintf(`{"type":"control_request","request_id":%q,"request":{"subtype":"initialize"}}`, requestID)
}

// initializeAck is the decode target for the emitted control_response. Written as a
// LITERAL here rather than reusing writeInitializeAck's own map, for the reason
// TestRunStreamJSON_InterruptAckRider gives: a target built from the producer follows
// a nesting bug green.
//
// The double nesting is the shape under test — `subtype`/`request_id` under
// `response` (the envelope inversion writeInterruptAck documents), and the initialize
// payload one level deeper still as `response.response`. Models decode into
// map[string]any so key PRESENCE is observable; a struct with typed fields cannot
// tell an absent key from a zero value, which is exactly the distinction AC2 exists
// to protect.
type initializeAck struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Models []map[string]any `json:"models"`
		} `json:"response"`
	} `json:"response"`
}

// answerInitialize feeds one `initialize` control_request through runStreamJSON in
// the given mode, asserts exactly one line came back, and decodes it. The emitted
// bytes are the subject on purpose: absence is measured after json.Marshal, where a
// consumer meets it, rather than in the fake's own map.
func answerInitialize(t *testing.T, requestID string, honorInterrupt bool) initializeAck {
	t.Helper()

	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(initializeControlRequestLine(requestID)+"\n"), &buf,
		honorInterrupt, false, "", false)

	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatalf("honorInterrupt=%v: the initialize control_request went unanswered (no output)",
			honorInterrupt)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("honorInterrupt=%v: line count got %d, want 1 (the control_response alone)\n%s",
			honorInterrupt, len(lines), out)
	}
	var ack initializeAck
	if err := json.Unmarshal([]byte(lines[0]), &ack); err != nil {
		t.Fatalf("unmarshal control_response line: %v\n%s", err, lines[0])
	}
	return ack
}

// modelEntryKeySet canonicalises one model entry to its sorted key names joined by
// ",". Used on BOTH sides of the cross-check below, so a bug in it cannot make one
// side vacuous on its own — and the "at least two distinct sets" gate in
// captureModelKeySets is what pins it as discriminating rather than collapsing.
func modelEntryKeySet(entry map[string]any) string {
	names := make([]string, 0, len(entry))
	for name := range entry {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// captureModelKeySets walks every committed capture the glob matches and returns the
// key sets its model entries actually carry, mapped to a provenance string for
// failure messages. The record's own models_entry_fields summary is deliberately NOT
// read: it is a union across entries and cannot see per-entry shape, which is the
// whole property this test exists to check.
//
// Every step is defensive about a missing or wrong-typed key — skip and continue,
// never a bare type assertion — because a sibling arm legitimately carries no
// control_responses at all. The three t.Fatalf gates are what keep that tolerance
// from turning into a vacuous pass.
//
// The provenance is the file's BASE NAME plus the entry's `value` (a public model
// identifier), and nothing else. The captures are live recordings from a real
// developer machine: their argv carries a home-directory path and their inner payload
// carries an account object. This walk reaches only `models`, and a failure message
// here must never grow into a dump of a decoded entry or a whole file.
func captureModelKeySets(t *testing.T) map[string]string {
	t.Helper()

	paths, err := filepath.Glob(initControlCaptureGlob)
	if err != nil {
		t.Fatalf("glob %q: %v", initControlCaptureGlob, err)
	}
	if len(paths) == 0 {
		t.Fatalf("glob %q matched no file: the committed capture moved or was renamed, and "+
			"a cross-check against nothing passes for any canned entry", initControlCaptureGlob)
	}

	sets := make(map[string]string)
	entries := 0
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		var record map[string]any
		if err := json.Unmarshal(b, &record); err != nil {
			t.Fatalf("unmarshal %s: %v", filepath.Base(path), err)
		}
		// A capture whose arm sent no control request carries no control_responses;
		// that contributes nothing and is not an error. The aggregate gates below are
		// what catch a walk that found nothing ANYWHERE.
		responses, ok := record["control_responses"].([]any)
		if !ok {
			continue
		}
		for _, response := range responses {
			outer, ok := response.(map[string]any)
			if !ok {
				continue
			}
			envelope, ok := outer["response"].(map[string]any)
			if !ok {
				continue
			}
			payload, ok := envelope["response"].(map[string]any)
			if !ok {
				continue
			}
			models, ok := payload["models"].([]any)
			if !ok {
				continue
			}
			for _, model := range models {
				entry, ok := model.(map[string]any)
				if !ok {
					continue
				}
				entries++
				value, _ := entry["value"].(string)
				sets[modelEntryKeySet(entry)] = fmt.Sprintf("%s entry %q", filepath.Base(path), value)
			}
		}
	}

	if entries == 0 {
		t.Fatalf("glob %q matched %d file(s) but the walk of control_responses[].response."+
			"response.models[] yielded no entry: the capture's shape moved under this test and "+
			"the cross-check would pass for any canned entry", initControlCaptureGlob, len(paths))
	}
	if len(sets) < 2 {
		t.Fatalf("the walk collected %d entr(ies) but only %d distinct key set(s): the capture "+
			"carries three, so either it changed or modelEntryKeySet collapses entries — and a "+
			"canonicaliser that collapses everything to one string makes the membership check "+
			"below pass for anything", entries, len(sets))
	}
	return sets
}

// initEffortLevels is the five reasoning-effort levels the capture's full-shape
// entries carry, in the capture's own order. A literal here, compared against the
// canned entry's own literal, so a level silently dropped from either shows up red.
var initEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// effortLevels reads a decoded entry's supportedEffortLevels as a []string, reporting
// whether the key is present AND carries a JSON array of strings. The two-value map
// lookup is deliberate: a zero-value comparison cannot tell an absent key from an
// empty one, and that confusion is what this ticket exists to prevent.
func effortLevels(entry map[string]any) ([]string, bool) {
	raw, ok := entry["supportedEffortLevels"]
	if !ok {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	levels := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		levels = append(levels, s)
	}
	return levels, true
}

// TestRunStreamJSON_InitializeControlAnswer is #1692 whole: the fake answers an
// `initialize` control_request in BOTH stream modes with the double-nested ack
// envelope, the canned list covers the present-and-absent arms a consumer branches
// on, every canned key set is one the committed capture actually carries, and a
// control line that is not an initialize request is unchanged.
func TestRunStreamJSON_InitializeControlAnswer(t *testing.T) {
	t.Parallel()

	t.Run("answers in both stream modes with the double-nested envelope", func(t *testing.T) {
		t.Parallel()

		// Both rows are load-bearing, and each is the sole red for the OPPOSITE
		// mis-gating. Measured 2026-08-24 over the two mutants: an arm placed inside
		// the honorInterrupt branch instead of beside it — which is where every other
		// control_request handling in this file lives, so not a hypothetical — leaves
		// the default-mode row the only red in this subtest, and the default mode is
		// what every existing e2e suite runs. An arm reached only when honorInterrupt
		// is false reddens the interrupt-mode row alone. Neither row substitutes for
		// the other.
		for _, tc := range []struct {
			name           string
			honorInterrupt bool
		}{
			{name: "default mode", honorInterrupt: false},
			{name: "interrupt mode", honorInterrupt: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				// A distinctive id, so the echo assertion cannot pass on a value the
				// fake could have minted itself.
				const reqID = "e2e-1692-initialize-req"
				ack := answerInitialize(t, reqID, tc.honorInterrupt)

				if ack.Type != "control_response" {
					t.Errorf("type: got %q, want %q", ack.Type, "control_response")
				}
				if ack.Response.Subtype != "success" {
					t.Errorf("response.subtype: got %q, want %q", ack.Response.Subtype, "success")
				}
				if ack.Response.RequestID != reqID {
					t.Errorf("response.request_id: got %q, want %q (the daemon's own id, echoed)",
						ack.Response.RequestID, reqID)
				}
				// The nesting, asserted in the negative: the capture puts NEITHER field
				// at the top level, and a top-level subtype is what would make
				// streamsup's streamLine.Subtype decode non-empty and send the daemon
				// down an emitSystemSubtype-shaped path that does not exist for this type.
				if ack.Subtype != "" {
					t.Errorf("top-level subtype: got %q, want empty — the capture nests subtype "+
						"under response", ack.Subtype)
				}
				// The payload level deeper still. An answer that put models beside
				// subtype/request_id would satisfy every row above.
				if len(ack.Response.Response.Models) == 0 {
					t.Error("response.response.models is empty: the initialize payload lives one " +
						"level below the ack envelope, and that is where the model list belongs")
				}
			})
		}
	})

	t.Run("covers both the present and the absent arm", func(t *testing.T) {
		t.Parallel()

		models := answerInitialize(t, "e2e-1692-arms", false).Response.Response.Models

		var full, minimal int
		for _, entry := range models {
			levels, hasLevels := effortLevels(entry)
			// Presence and value read SEPARATELY. entry["…"].(bool) alone would
			// return (false, false) for an absent key and for a present non-bool
			// alike, which is the very conflation the absent arm exists to rule out.
			raw, hasAuto := entry["supportsAutoMode"]
			autoOn, _ := raw.(bool)
			switch {
			case hasLevels && hasAuto:
				if autoOn && slices.Equal(levels, initEffortLevels) {
					full++
				}
			case !hasLevels && !hasAuto:
				minimal++
			}
		}
		if full == 0 {
			t.Errorf("no canned entry carries supportedEffortLevels %v with supportsAutoMode true: "+
				"a consumer's per-model branch has no present case to exercise", initEffortLevels)
		}
		if minimal == 0 {
			t.Error("no canned entry omits BOTH supportedEffortLevels and supportsAutoMode as JSON " +
				"keys: present-and-empty or present-and-false is a different INPUT from absent, and " +
				"emitting one hands the decode an already-collapsed value, so the absent-key arm — " +
				"which turnevent.ModelOption.EffortLevels reads as nil (#1828) and " +
				"turnevent.ModelOption.SupportsAutoMode reads as false (#1819) — goes unexercised")
		}
	})

	t.Run("every canned key set occurs in the committed capture", func(t *testing.T) {
		t.Parallel()

		canned := answerInitialize(t, "e2e-1692-shapes", false).Response.Response.Models
		// Without this the loop below passes having compared nothing — and two arms is
		// the deliverable, not an incidental count.
		if len(canned) < 2 {
			t.Fatalf("the canned answer carries %d model entr(ies), want at least 2 (a full-shape "+
				"arm and a minimal one)", len(canned))
		}

		capture := captureModelKeySets(t)
		attested := make([]string, 0, len(capture))
		for set, provenance := range capture {
			attested = append(attested, fmt.Sprintf("[%s] — %s", set, provenance))
		}
		sort.Strings(attested)

		for _, entry := range canned {
			set := modelEntryKeySet(entry)
			if _, ok := capture[set]; !ok {
				value, _ := entry["value"].(string)
				t.Errorf("canned entry %q carries key set [%s], which occurs in no committed "+
					"capture entry — the shape is invented rather than transcribed. The capture "+
					"carries:\n  %s", value, set, strings.Join(attested, "\n  "))
			}
		}
	})

	t.Run("a control request of another subtype is unchanged", func(t *testing.T) {
		t.Parallel()

		// The sole red for a predicate matching on `type` alone. The rest of AC4 is
		// held by two existing tests that must stay green UNMODIFIED —
		// TestRunStreamJSON_NonUserLinesIgnored (interrupt + blank + unparsable line →
		// zero bytes in default mode) and TestRunStreamJSON_InterruptAckRider (interrupt
		// mode still emits its ack then the interrupted result, in that order) — so
		// their rows are not restated here.
		const unknown = `{"type":"control_request","request_id":"r1",` +
			`"request":{"subtype":"e2e-1692-not-initialize"}}`
		var buf bytes.Buffer
		runStreamJSON(strings.NewReader(unknown+"\n"), &buf, false, false, "", false)

		if buf.Len() != 0 {
			t.Fatalf("an unknown control_request subtype produced %d bytes of output, want 0: %q",
				buf.Len(), buf.String())
		}
	})
}
