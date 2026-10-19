//go:build e2e_realclaude

package realclaude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const tttMobileMethod = "de.pyryco.mobile.e2e.InteractiveStreamE2ETest#interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy"
const tttMobilePrefix = "testdata/tool_then_text_mobile_stream_event_v"

type tttMobileEvidence struct {
	Method         string `json:"method"`
	DaemonRevision string `json:"daemon_revision"`
	MobileRevision string `json:"mobile_revision"`
	Executed       int    `json:"executed"`
	Passed         int    `json:"passed"`
	Failed         int    `json:"failed"`
	Skipped        int    `json:"skipped"`
	XMLSHA256      string `json:"xml_sha256"`
	XML            string `json:"counted_xml"`
}

func tttCountMobileXML(blob []byte) (tttMobileEvidence, error) {
	e := tttMobileEvidence{Method: tttMobileMethod, XML: string(blob), XMLSHA256: fmt.Sprintf("%x", sha256.Sum256(blob))}
	decoder := xml.NewDecoder(strings.NewReader(string(blob)))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return e, errors.New("invalid mobile XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "testcase" {
			continue
		}
		var testcase struct {
			Class   string     `xml:"classname,attr"`
			Name    string     `xml:"name,attr"`
			Failure []struct{} `xml:"failure"`
			Error   []struct{} `xml:"error"`
			Skipped []struct{} `xml:"skipped"`
		}
		if decoder.DecodeElement(&testcase, &start) != nil {
			return e, errors.New("invalid mobile testcase")
		}
		if testcase.Class+"#"+testcase.Name != tttMobileMethod {
			return e, errors.New("XML contains a different method")
		}
		if len(testcase.Skipped) > 0 {
			e.Skipped++
			continue
		}
		e.Executed++
		if len(testcase.Failure)+len(testcase.Error) > 0 {
			e.Failed++
		} else {
			e.Passed++
		}
	}
	if e.Executed != 1 || e.Skipped != 0 {
		return e, errors.New("named mobile method must execute exactly once without skipping")
	}
	return e, nil
}

func tttMobileAdmit(r tttRecord, e tttMobileEvidence, daemon, mobile string) error {
	if r.Source != "mobile-daemon" || !r.complete() || r.DaemonDirty || r.ClaudeVersion == "" {
		return errors.New("missing mobile provenance or complete clean recording")
	}
	if r.DaemonRevision != daemon || e.DaemonRevision != daemon || len(daemon) != 40 ||
		r.MobileRevision != mobile || e.MobileRevision != mobile || len(mobile) != 40 {
		return errors.New("tested revisions do not match the capture source")
	}
	for _, revision := range []string{daemon, mobile} {
		if _, err := hex.DecodeString(revision); err != nil {
			return errors.New("invalid revision pin")
		}
	}
	if e.Method != tttMobileMethod || e.Executed != 1 || e.Skipped != 0 || e.Passed+e.Failed != 1 || e.Passed < 0 || e.Failed < 0 || len(e.XMLSHA256) != 64 {
		return errors.New("missing counted named mobile execution")
	}
	counted, err := tttCountMobileXML([]byte(e.XML))
	if err != nil || counted.XMLSHA256 != e.XMLSHA256 || counted.Executed != e.Executed || counted.Passed != e.Passed || counted.Failed != e.Failed {
		return errors.New("counted XML does not match retained evidence")
	}
	if r.Prompt != tttPrompt || r.ToolStarts != 1 || r.ToolCompletions != 1 || r.TextChunks == 0 {
		return errors.New("mobile tool-then-text scenario was not captured")
	}
	return nil
}

func tttReadMobileFixture(dir string) (tttRecord, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "tool_then_text_mobile_stream_event_v*.json"))
	if err != nil || len(paths) != 1 {
		return tttRecord{}, errors.New("missing unambiguous mobile fixture")
	}
	blob, err := os.ReadFile(paths[0])
	if err != nil {
		return tttRecord{}, errors.New("read mobile fixture")
	}
	var rec tttRecord
	if json.Unmarshal(blob, &rec) != nil || rec.MobileEvidence == nil {
		return rec, errors.New("mobile fixture lacks coupled evidence")
	}
	versionPath, versionErr := secapFixturePath(rec.ClaudeVersion)
	if versionErr != nil || filepath.Base(strings.Replace(versionPath, secapFixturePrefix, tttMobilePrefix, 1)) != filepath.Base(paths[0]) {
		return rec, errors.New("mobile fixture version pin mismatch")
	}
	evidenceBlob, err := os.ReadFile(strings.Replace(paths[0], "mobile_stream_event", "mobile_evidence", 1))
	var evidence tttMobileEvidence
	if err != nil || json.Unmarshal(evidenceBlob, &evidence) != nil || !reflect.DeepEqual(&evidence, rec.MobileEvidence) {
		return rec, errors.New("mobile evidence companion mismatch")
	}
	return rec, tttMobileAdmit(rec, evidence, rec.DaemonRevision, rec.MobileRevision)
}

// Diagnostic admission preserves a run even when it fails. Completion additionally
// needs the cause established by the coupled failing-before/passing-after replay;
// a successful mobile rerun alone cannot settle the historical rejection.
func tttMobileProofComplete(rec tttRecord) bool {
	return rec.Source == "mobile-daemon" && rec.EstablishedGuard != "" &&
		len(rec.RejectedRows) == 0 && rec.MobileEvidence != nil &&
		rec.MobileEvidence.Executed == 1 && rec.MobileEvidence.Passed == 1 &&
		rec.MobileEvidence.Failed == 0 && rec.MobileEvidence.Skipped == 0
}

func tttMobileCapture(t *testing.T) {
	t.Helper()
	dir := os.Getenv("PYRY_TOOL_THEN_TEXT_MOBILE_DIR")
	if dir == "" {
		rec, err := tttReadMobileFixture("testdata")
		if err != nil {
			t.Fatal("required mobile capture is missing or invalid; run: python3 internal/e2e/realclaude/tool_then_text_mobile_capture.py --mobile /absolute/mobile --mobile-agents /absolute/mobile-agents --daemon-agents /absolute/pyrycode-agents")
		}
		_, rejected := tttReplay(t, rec.Rows)
		if !reflect.DeepEqual(rejected, rec.RejectedRows) {
			t.Fatal("mobile fixture rejection census changed; identify guard and add focused proof before promotion")
		}
		if !tttMobileProofComplete(rec) {
			t.Fatal("mobile diagnostic evidence remains; a successful rerun alone is not diagnosis: establish the rejecting guard with focused replay, repair it, and execute the focused mobile pass")
		}
		return
	}
	var driver struct {
		DaemonRevision string `json:"daemon_revision"`
		MobileRevision string `json:"mobile_revision"`
		XMLFile        string `json:"xml_file"`
		ExitCode       int    `json:"exit_code"`
	}
	driverBlob, err := os.ReadFile(filepath.Join(dir, "driver.json"))
	if err != nil || json.Unmarshal(driverBlob, &driver) != nil {
		t.Fatal("missing portable mobile driver manifest")
	}
	if filepath.Base(driver.XMLFile) != driver.XMLFile {
		t.Fatal("XML path must be a recovery basename")
	}
	xmlBlob, err := os.ReadFile(filepath.Join(dir, driver.XMLFile))
	if err != nil {
		t.Fatal("missing mobile XML")
	}
	evidence, err := tttCountMobileXML(xmlBlob)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Passed == 1 && driver.ExitCode != 0 {
		t.Fatal("launcher failed despite a passed testcase")
	}
	evidence.DaemonRevision, evidence.MobileRevision = driver.DaemonRevision, driver.MobileRevision
	paths, _ := filepath.Glob(filepath.Join(dir, "*.manifest.json"))
	selected := 0
	var promotedPath string
	var promotedRecord, promotedEvidence []byte
	for _, path := range paths {
		var manifest struct {
			Source         string   `json:"source"`
			DaemonRevision string   `json:"daemon_revision"`
			DaemonDirty    bool     `json:"daemon_dirty"`
			ClaudeVersion  string   `json:"claude_version"`
			SpawnShape     []string `json:"spawn_shape"`
			Home           string   `json:"home"`
			Workdir        string   `json:"workdir"`
			BytesDropped   int      `json:"bytes_dropped"`
			Tail           int      `json:"unterminated_tail_bytes"`
			IOFailed       bool     `json:"io_failed"`
			CapturedBytes  int      `json:"captured_bytes"`
			StdoutSHA256   string   `json:"stdout_sha256"`
			Rejections     []string `json:"rejections"`
		}
		blob, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(blob, &manifest) != nil {
			t.Fatal("invalid daemon capture manifest")
		}
		rawPath := strings.TrimSuffix(path, ".manifest.json") + ".stdout"
		stat, err := os.Stat(rawPath)
		if err != nil || stat.Size() > 8<<20 {
			t.Fatal("missing or oversized raw capture")
		}
		raw, err := os.ReadFile(rawPath)
		if err != nil {
			t.Fatal("read raw capture")
		}
		if !strings.Contains(string(raw), tttPrompt) {
			continue
		}
		selected++
		if manifest.IOFailed || manifest.BytesDropped != 0 || manifest.Tail != 0 || manifest.CapturedBytes != len(raw) || manifest.StdoutSHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) {
			t.Fatal("lossy, incomplete or corrupted mobile input recording")
		}
		rows := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		fullEvents, _ := tttReplay(t, rows)
		if !reflect.DeepEqual(tttRejections(fullEvents), manifest.Rejections) {
			t.Fatal("live parser and original wire replay rejections differ")
		}
		promptSeen, result := false, -1
		for i, row := range rows {
			if strings.Contains(row, tttPrompt) {
				promptSeen = true
			}
			if promptSeen && secapTypeField([]byte(row)) == "result" {
				result = i
				break
			}
		}
		if result < 0 {
			t.Fatal("mobile prompt did not reach result")
		}
		rows = rows[:result+1]
		events, rejected := tttReplay(t, rows)
		red := newDropcapRedactor(manifest.Home, dir, manifest.Workdir, "", secapSessionID, time.Now().UnixNano())
		scanner := newDropcapScanner(manifest.Home, dir, manifest.Workdir)
		retained := tttRedactRows(rows, red, &scanner)
		retainedEvents, retainedRejected := tttReplay(t, retained)
		retainedEvidence := evidence
		retainedEvidence.XML = red.str(evidence.XML)
		retainedEvidence.XMLSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(retainedEvidence.XML)))
		version, _, _ := strings.Cut(strings.TrimSpace(manifest.ClaudeVersion), " ")
		rec := tttRecord{Ticket: "2939", Source: manifest.Source, CapturedAt: time.Now().UTC().Format(time.RFC3339), ClaudeVersion: version, DaemonRevision: manifest.DaemonRevision, DaemonDirty: manifest.DaemonDirty, MobileRevision: driver.MobileRevision, Prompt: tttPrompt, SpawnShape: red.strs(manifest.SpawnShape), Rows: retained, RejectedRows: rejected, MobileEvidence: &retainedEvidence, Assumptions: "Observed production mobile daemon argv and ordered init/control rows; absent effort remains unobserved. No capture model/permission override.", ReplayMatches: reflect.DeepEqual(rejected, retainedRejected) && reflect.DeepEqual(tttRejections(events), tttRejections(retainedEvents)), Outcome: "mobile diagnostic capture; identify rejecting guard before parser repair"}
		for _, ev := range events {
			switch ev := ev.(type) {
			case turnevent.ToolStart:
				rec.ToolStarts++
			case turnevent.ToolUpdate:
				if ev.Status == turnevent.ToolStatusCompleted {
					rec.ToolCompletions++
				}
			case turnevent.TextChunk:
				rec.TextChunks++
			}
		}
		if err := tttMobileAdmit(rec, retainedEvidence, driver.DaemonRevision, driver.MobileRevision); err != nil {
			t.Fatal(err)
		}
		rec.Redaction, rec.ScanApplied = red.substitutions(), scanner.applied()
		blob, err = json.Marshal(rec)
		if err != nil {
			t.Fatal("marshal mobile fixture")
		}
		hits, skipped := scanner.scan(blob)
		if len(hits) > 0 {
			t.Fatalf("mobile deny-scan rejected classes %v", hits)
		}
		rec.ScanSkipped = skipped
		blob, err = json.Marshal(rec)
		if err != nil {
			t.Fatal("marshal scan metadata")
		}
		if hits, _ := scanner.scan(blob); len(hits) > 0 {
			t.Fatalf("final deny-scan rejected classes %v", hits)
		}
		evidenceBlob, err := json.Marshal(retainedEvidence)
		if err != nil {
			t.Fatal("marshal counted mobile evidence")
		}
		fixture, err := secapFixturePath(rec.ClaudeVersion)
		if err != nil {
			t.Fatal("unsafe captured version")
		}
		fixture = strings.Replace(fixture, secapFixturePrefix, tttMobilePrefix, 1)
		promotedPath, promotedRecord, promotedEvidence = fixture, blob, evidenceBlob
		t.Logf("mobile rows=%d rejection_indices=%v named executed/passed/failed/skipped=%d/%d/%d/%d", len(retained), rejected, evidence.Executed, evidence.Passed, evidence.Failed, evidence.Skipped)
		t.Error("diagnostic capture retained; identify rejecting guard before repair, then run focused mobile proof; successful rerun alone is not diagnosis")
	}
	if selected != 1 {
		t.Fatal("capture must identify exactly one child carrying the unchanged mobile prompt")
	}
	// Recovery precedes promotion. Existing mobile input may be the only record
	// of the rejecting sequence, so a subsequent passing turn cannot overwrite it.
	if err := os.WriteFile(filepath.Join(dir, "mobile-redacted.json"), append(promotedRecord, '\n'), 0o600); err != nil {
		t.Fatal("write mobile redacted recovery")
	}
	if _, err := os.Stat(promotedPath); !os.IsNotExist(err) {
		t.Fatal("mobile fixture already exists; retain fresh recovery and preserve the original sequence")
	}
	for _, output := range []struct {
		path string
		data []byte
	}{
		{promotedPath, promotedRecord},
		{strings.Replace(promotedPath, "mobile_stream_event", "mobile_evidence", 1), promotedEvidence},
	} {
		if err := os.WriteFile(output.path, append(output.data, '\n'), 0o600); err != nil {
			t.Fatal("write mobile recovery/fixture")
		}
	}
	t.Logf("commit mobile fixture and evidence: %s", promotedPath)
}
