//go:build e2e_realclaude

package realclaude

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolThenTextSharedHarnessXML(t *testing.T) {
	blob := []byte(`<testsuite><testcase classname="other" name="earlier"><failure/></testcase><testcase classname="de.pyryco.mobile.e2e.InteractiveStreamE2ETest" name="interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy"/><testcase classname="other" name="later"><skipped/></testcase></testsuite>`)
	e, err := tttCountHarnessXML(blob)
	if err != nil || e.Executed != 2 || e.Passed != 1 || e.Failed != 1 || e.Skipped != 1 || len(e.Cases) != 3 || e.Cases[0].Method != "other#earlier" {
		t.Fatalf("mixed-method diagnostic lost observations: %+v, %v", e, err)
	}
	if _, err := tttCountMobileXML(blob); err == nil {
		t.Fatal("mixed-method diagnostic satisfied focused proof")
	}
}

func TestToolThenTextSharedHarnessChildren(t *testing.T) {
	dir := t.TempDir()
	daemon, mobile := strings.Repeat("a", 40), strings.Repeat("b", 40)
	xml := `<testsuite><testcase classname="other" name="earlier"><failure/></testcase><testcase classname="de.pyryco.mobile.e2e.InteractiveStreamE2ETest" name="interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy"/></testsuite>`
	driver := tttHarnessDriver{Mode: "shared-harness", DaemonRevision: daemon, MobileRevision: mobile, XMLFile: "mobile.xml", Selected: []string{"other#earlier", tttMobileMethod}}
	write := func(name string, value any) {
		t.Helper()
		blob, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.json", driver)
	if err := os.WriteFile(filepath.Join(dir, "mobile.xml"), []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, rows := range [][]string{
		{`{"type":"system","subtype":"init","session_id":"session","messaging_socket_path":"/tmp/private-2939.sock"}`, `{"type":"control_response","response":{"subtype":"success","request_id":"request"}}`, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"unattributed"}}}`, `{"type":"result"}`, `{"type":"future_kind"}`},
		{fmt.Sprintf(`{"type":"user","message":{"content":%q}}`, tttPrompt), `{"type":"result"}`},
	} {
		raw := strings.Join(rows, "\n") + "\n"
		if i == 1 {
			raw += `{"unfinished":`
		}
		events, _ := tttReplay(t, rows)
		m := tttChildManifest{Source: "mobile-daemon", DaemonRevision: daemon, ClaudeVersion: "2.1.280", CapturedBytes: len(raw), StdoutSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(raw))), Rejections: tttRejections(events)}
		if i == 1 {
			m.Tail = len(`{"unfinished":`)
		}
		base := fmt.Sprintf("daemon-%d", i)
		write(base+".manifest.json", m)
		if err := os.WriteFile(filepath.Join(dir, base+".stdout"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := tttReadHarness(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Children) != 2 || len(bundle.Children[0].Rows) != 5 || len(bundle.Children[0].RejectedRows) != 2 || bundle.Children[0].RejectedRows[0] != 2 || !bundle.Children[0].ReplayMatches || bundle.Children[0].TargetPrompt {
		t.Fatalf("rejecting non-prompt child or post-result row discarded: %+v", bundle.Children)
	}
	if bundle.Children[1].Complete || bundle.Children[1].Tail != `{"unfinished":` || bundle.Children[1].Manifest.Tail == 0 || !bundle.Children[1].TargetPrompt {
		t.Fatal("incomplete child lost its explicit tail or became cause evidence")
	}
	if len(bundle.Children[0].ResultRows) != 1 || bundle.Children[0].ResultRows[0] != 3 {
		t.Fatal("result boundary lost")
	}
	blob, _ := json.Marshal(bundle)
	if strings.Contains(string(blob), "/tmp/private-2939.sock") {
		t.Fatal("dynamic socket leaked")
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon-orphan.stdout"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tttReadHarness(t, dir); err == nil {
		t.Fatal("orphan child silently omitted")
	}
}
