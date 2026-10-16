//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolThenTextMobileAdmission(t *testing.T) {
	daemon, mobile := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name   string
		change func(*tttRecord, *tttMobileEvidence)
		want   bool
	}{
		{"valid pass", func(*tttRecord, *tttMobileEvidence) {}, true},
		{"diagnostic failure", func(_ *tttRecord, e *tttMobileEvidence) { e.Passed = 0; e.Failed = 1 }, true},
		{"isolated", func(r *tttRecord, _ *tttMobileEvidence) { r.Source = "isolated" }, false},
		{"missing evidence", func(_ *tttRecord, e *tttMobileEvidence) { *e = tttMobileEvidence{} }, false},
		{"skipped", func(_ *tttRecord, e *tttMobileEvidence) { e.Executed = 0; e.Passed = 0; e.Skipped = 1 }, false},
		{"wrong method", func(_ *tttRecord, e *tttMobileEvidence) { e.Method = "other" }, false},
		{"wrong daemon", func(r *tttRecord, _ *tttMobileEvidence) { r.DaemonRevision = mobile }, false},
		{"dirty binary", func(r *tttRecord, _ *tttMobileEvidence) { r.DaemonDirty = true }, false},
		{"wrong mobile", func(_ *tttRecord, e *tttMobileEvidence) { e.MobileRevision = daemon }, false},
		{"loss", func(r *tttRecord, _ *tttMobileEvidence) { r.Loss.Bytes = 1 }, false},
		{"tail", func(r *tttRecord, _ *tttMobileEvidence) { r.Loss.Tail = 1 }, false},
		{"wrong counts", func(_ *tttRecord, e *tttMobileEvidence) { e.Passed = 2 }, false},
		{"no XML pin", func(_ *tttRecord, e *tttMobileEvidence) { e.XMLSHA256 = "" }, false},
		{"changed XML", func(_ *tttRecord, e *tttMobileEvidence) { e.XML = `<testsuite/>` }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tttRecord{Source: "mobile-daemon", Prompt: tttPrompt, DaemonRevision: daemon, MobileRevision: mobile, ClaudeVersion: "test-version", Rows: []string{`{"type":"result"}`}, ReplayMatches: true, ToolStarts: 1, ToolCompletions: 1, TextChunks: 1}
			body := ""
			if tc.name == "diagnostic failure" {
				body = "<failure/>"
			}
			e, err := tttCountMobileXML([]byte(`<testsuite><testcase classname="de.pyryco.mobile.e2e.InteractiveStreamE2ETest" name="interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy">` + body + `</testcase></testsuite>`))
			if err != nil {
				t.Fatal(err)
			}
			e.DaemonRevision, e.MobileRevision = daemon, mobile
			tc.change(&r, &e)
			if got := tttMobileAdmit(r, e, daemon, mobile) == nil; got != tc.want {
				t.Fatalf("admitted=%v want %v", got, tc.want)
			}
		})
	}
}

func TestToolThenTextMobileXML(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"passed", "", true},
		{"failed", "<failure message=\"sentinel\"/>", true},
		{"skipped", "<skipped/>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob := []byte(`<testsuite><testcase classname="de.pyryco.mobile.e2e.InteractiveStreamE2ETest" name="interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy">` + tc.body + `</testcase></testsuite>`)
			e, err := tttCountMobileXML(blob)
			if (err == nil) != tc.want {
				t.Fatalf("XML admission err=%v", err)
			}
			if tc.name == "failed" && (e.Passed != 0 || e.Failed != 1) {
				t.Fatal("diagnostic failure was treated as a pass")
			}
		})
	}
	for _, blob := range []string{`<testsuite/>`, `<testsuite><testcase classname="other" name="other"/></testsuite>`, `<broken`} {
		if _, err := tttCountMobileXML([]byte(blob)); err == nil {
			t.Fatal("empty, wrong or malformed XML admitted")
		}
	}
}

func TestToolThenTextIsolatedDoesNotDisarmMobile(t *testing.T) {
	dir := t.TempDir()
	blob, err := os.ReadFile("testdata/tool_then_text_wire_stream_event_v2.1.280.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool_then_text_wire_stream_event_v2.1.280.json"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tttReadMobileFixture(dir); err == nil {
		t.Fatal("isolated evidence satisfied missing mobile evidence")
	}
	// Even a mobile-looking filename does not confer mobile provenance.
	var rec tttRecord
	if err := json.Unmarshal(blob, &rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool_then_text_mobile_stream_event_v2.1.280.json"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tttReadMobileFixture(dir); err == nil {
		t.Fatal("renamed isolated evidence was admitted")
	}
}

func TestToolThenTextMobileSuccessfulRerunIsNotDiagnosis(t *testing.T) {
	rec := tttRecord{Source: "mobile-daemon", MobileEvidence: &tttMobileEvidence{Executed: 1, Passed: 1}}
	if tttMobileProofComplete(rec) {
		t.Fatal("passing mobile rerun discharged unknown historical cause")
	}
}
