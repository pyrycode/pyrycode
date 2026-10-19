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
)

type tttHarnessCase struct {
	Method  string `json:"method"`
	Outcome string `json:"outcome"`
}

type tttHarnessXML struct {
	Executed int              `json:"executed"`
	Passed   int              `json:"passed"`
	Failed   int              `json:"failed"`
	Skipped  int              `json:"skipped"`
	Cases    []tttHarnessCase `json:"report_order"`
	XML      string           `json:"counted_xml"`
	SHA256   string           `json:"xml_sha256"`
}

// Diagnostic counts include every reported method. Focused admission continues
// to use tttCountMobileXML's exactly-one named execution requirement.
func tttCountHarnessXML(blob []byte) (tttHarnessXML, error) {
	e := tttHarnessXML{XML: string(blob), SHA256: fmt.Sprintf("%x", sha256.Sum256(blob))}
	d := xml.NewDecoder(strings.NewReader(string(blob)))
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return e, errors.New("invalid harness XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "testcase" {
			continue
		}
		var c struct {
			Class   string     `xml:"classname,attr"`
			Name    string     `xml:"name,attr"`
			Failure []struct{} `xml:"failure"`
			Error   []struct{} `xml:"error"`
			Skipped []struct{} `xml:"skipped"`
		}
		if d.DecodeElement(&c, &start) != nil || c.Class == "" || c.Name == "" {
			return e, errors.New("invalid harness testcase")
		}
		outcome := "passed"
		switch {
		case len(c.Failure)+len(c.Error) > 0:
			e.Executed++
			e.Failed++
			outcome = "failed"
		case len(c.Skipped) > 0:
			e.Skipped++
			outcome = "skipped"
		default:
			e.Executed++
			e.Passed++
		}
		e.Cases = append(e.Cases, tttHarnessCase{c.Class + "#" + c.Name, outcome})
	}
	if len(e.Cases) == 0 || len(e.Cases) > 256 {
		return e, errors.New("missing or oversized harness testcase inventory")
	}
	return e, nil
}

type tttExecutionRecord struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Method    string `json:"method"`
}

type tttHarnessDriver struct {
	Mode           string                     `json:"mode"`
	DaemonRevision string                     `json:"daemon_revision"`
	MobileRevision string                     `json:"mobile_revision"`
	XMLFile        string                     `json:"xml_file"`
	ExitCode       int                        `json:"exit_code"`
	Selected       []string                   `json:"selected_methods"`
	Execution      []tttExecutionRecord       `json:"execution_records"`
	Provenance     map[string]json.RawMessage `json:"provenance"`
}

type tttChildManifest struct {
	Source         string   `json:"source"`
	DaemonRevision string   `json:"daemon_revision"`
	DaemonDirty    bool     `json:"daemon_dirty"`
	ClaudeVersion  string   `json:"claude_version"`
	SpawnShape     []string `json:"spawn_shape"`
	Home           string   `json:"home"`
	Workdir        string   `json:"workdir"`
	StartedAt      string   `json:"started_at"`
	FinishedAt     string   `json:"finished_at"`
	BytesDropped   int      `json:"bytes_dropped"`
	Tail           int      `json:"unterminated_tail_bytes"`
	IOFailed       bool     `json:"io_failed"`
	CapturedBytes  int      `json:"captured_bytes"`
	StdoutSHA256   string   `json:"stdout_sha256"`
	Rejections     []string `json:"rejections"`
}

type tttChildEvidence struct {
	Child             string           `json:"child"`
	Manifest          tttChildManifest `json:"manifest"`
	Rows              []string         `json:"ordered_stdout"`
	Tail              string           `json:"unterminated_tail"`
	TargetPrompt      bool             `json:"contains_target_prompt"`
	ResultRows        []int            `json:"result_rows"`
	RejectedRows      []int            `json:"rejected_rows"`
	ReplayMatches     bool             `json:"retained_replay_matches_original"`
	Complete          bool             `json:"lossless_through_result"`
	LiveReplayMatches bool             `json:"live_replay_matches_original"`
	ReplayRejections  []string         `json:"original_replay_rejections"`
}

type tttHarnessEvidence struct {
	Driver          tttHarnessDriver      `json:"driver"`
	XML             tttHarnessXML         `json:"mobile_evidence"`
	Children        []tttChildEvidence    `json:"children"`
	ContextComplete bool                  `json:"context_complete"`
	Limitations     string                `json:"limitations"`
	Redaction       []dropcapSubstitution `json:"redaction"`
	ScanApplied     map[string]bool       `json:"credential_scan_applied"`
	ScanSkipped     []string              `json:"credential_scan_skipped"`
}

func tttReadBounded(dir, name string, limit int64) ([]byte, error) {
	if name == "." || filepath.Base(name) != name {
		return nil, errors.New("invalid recovery basename")
	}
	p := filepath.Join(dir, name)
	stat, err := os.Lstat(p)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > limit {
		return nil, errors.New("missing or oversized regular recovery file")
	}
	return os.ReadFile(p)
}

// Every child is retained, independent of prompt, method outcome or rejection.
// An incomplete child is diagnostic evidence, never an admissible cause fixture.
func tttReadHarness(t *testing.T, dir string) (tttHarnessEvidence, error) {
	t.Helper()
	var b tttHarnessEvidence
	bad := func(category string) (tttHarnessEvidence, error) { return b, errors.New(category) }
	driver, err := tttReadBounded(dir, "driver.json", 1<<20)
	if err != nil || json.Unmarshal(driver, &b.Driver) != nil || b.Driver.Mode != "shared-harness" {
		return bad("missing shared-harness driver")
	}
	xmlBlob, err := tttReadBounded(dir, b.Driver.XMLFile, 1<<20)
	if err != nil {
		return bad("missing shared-harness counted XML")
	}
	b.XML, err = tttCountHarnessXML(xmlBlob)
	if err != nil {
		return b, err
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "daemon-*.stdout"))
	manifests, _ := filepath.Glob(filepath.Join(dir, "daemon-*.manifest.json"))
	if len(paths) == 0 || len(paths) > 256 || len(paths) != len(manifests) {
		return bad("missing child pair or oversized child inventory")
	}
	all := []string{}
	red := newDropcapRedactor("", dir, "", "", secapSessionID, time.Now().UnixNano())
	scanner := newDropcapScanner("", dir, "")
	total := 0
	for _, path := range paths {
		name := filepath.Base(path)
		child := tttChildEvidence{Child: strings.TrimSuffix(name, ".stdout"), ResultRows: []int{}}
		blob, err := tttReadBounded(dir, child.Child+".manifest.json", 1<<20)
		if err != nil || json.Unmarshal(blob, &child.Manifest) != nil {
			return bad("missing child manifest")
		}
		m := child.Manifest
		if m.Source != "mobile-daemon" || m.DaemonRevision != b.Driver.DaemonRevision || m.DaemonDirty || len(m.DaemonRevision) != 40 || len(b.Driver.MobileRevision) != 40 || m.ClaudeVersion == "" {
			return bad("child provenance mismatch")
		}
		for _, revision := range []string{m.DaemonRevision, b.Driver.MobileRevision} {
			if _, err := hex.DecodeString(revision); err != nil {
				return bad("invalid child revision")
			}
		}
		raw, err := tttReadBounded(dir, name, 8<<20)
		if err != nil {
			return bad("missing child stdout")
		}
		total += len(raw)
		if total > 16<<20 {
			return bad("aggregate child input exceeds diagnostic bound")
		}
		if m.CapturedBytes != len(raw) || m.StdoutSHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) {
			return bad("child byte/hash mismatch")
		}
		parts := strings.Split(string(raw), "\n")
		child.Rows, child.Tail = parts[:len(parts)-1], parts[len(parts)-1]
		if m.Tail != len(child.Tail) || m.BytesDropped < 0 {
			return bad("child tail/loss counter mismatch")
		}
		child.TargetPrompt = strings.Contains(string(raw), tttPrompt)
		events, rejected := tttReplay(t, child.Rows)
		child.RejectedRows = rejected
		child.ReplayRejections = tttRejections(events)
		child.LiveReplayMatches = reflect.DeepEqual(child.ReplayRejections, m.Rejections)
		for i, row := range child.Rows {
			if secapTypeField([]byte(row)) == "result" {
				child.ResultRows = append(child.ResultRows, i)
			}
		}
		child.Complete = !m.IOFailed && m.BytesDropped == 0 && m.Tail == 0 && child.LiveReplayMatches && len(child.ResultRows) > 0
		// Add every child's dynamic roots before redacting the shared identity map.
		for _, p := range []string{m.Home, m.Workdir} {
			red.addPathClass("dynamic_path", "$DYNAMIC_PATH", p)
			scanner.addDynamicPath("dynamic_path", p)
		}
		all = append(all, string(blob))
		all = append(all, child.Rows...)
		all = append(all, child.Tail)
		b.Children = append(b.Children, child)
	}
	all = tttRedactRows(all, red, &scanner)
	offset := 0
	for i := range b.Children {
		c := &b.Children[i]
		if json.Unmarshal([]byte(all[offset]), &c.Manifest) != nil {
			return bad("redacted child manifest malformed")
		}
		offset++
		c.Rows = all[offset : offset+len(c.Rows)]
		offset += len(c.Rows)
		c.Tail = all[offset]
		offset++
		events, rejected := tttReplay(t, c.Rows)
		c.ReplayMatches = reflect.DeepEqual(c.RejectedRows, rejected) && reflect.DeepEqual(tttRejections(events), c.ReplayRejections)
		if !c.ReplayMatches {
			return bad("redaction changed child rejection census")
		}
	}
	b.XML.XML = red.str(b.XML.XML)
	b.XML.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(b.XML.XML)))
	b.ContextComplete = len(b.Driver.Selected) == 64 && len(b.XML.Cases) == 64 && b.XML.Skipped == 0
	started, finished, reported := map[string]int{}, map[string]int{}, map[string]int{}
	for _, e := range b.Driver.Execution {
		switch e.Event {
		case "started":
			started[e.Method]++
		case "finished":
			finished[e.Method]++
		}
	}
	for _, c := range b.XML.Cases {
		reported[c.Method]++
	}
	for _, method := range b.Driver.Selected {
		b.ContextComplete = b.ContextComplete && started[method] == 1 && finished[method] == 1 && reported[method] == 1
	}
	for _, c := range b.Children {
		b.ContextComplete = b.ContextComplete && c.Complete
	}
	b.Limitations = "Diagnostic only. XML retains report order; execution records retain observed TestRunner order. Child timestamps and session/message/parent identities permit correlation but do not attribute a whole-thread sentinel to a prompt. Historical operator settings remain unknown. Missing order records, loss, tails or no result make context incomplete. No established rejecting guard or parser repair is asserted."
	b.Redaction, b.ScanApplied = red.substitutions(), scanner.applied()
	blob, err := json.Marshal(b)
	if err != nil {
		return bad("encode shared-harness evidence")
	}
	hits, skipped := scanner.scan(blob)
	if len(hits) > 0 {
		return bad("shared-harness deny-scan rejected evidence")
	}
	b.ScanSkipped = skipped
	return b, nil
}

func TestRealClaude_ToolThenTextSharedHarnessCapture(t *testing.T) {
	dir := os.Getenv("PYRY_TOOL_THEN_TEXT_MOBILE_DIR")
	if dir == "" {
		t.Skip("shared-harness producer runs only through the managed Android route")
	}
	b, err := tttReadHarness(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(b)
	if err != nil {
		t.Fatal("encode harness recovery")
	}
	if err := os.WriteFile(filepath.Join(dir, "shared-harness-redacted.json"), append(blob, '\n'), 0o600); err != nil {
		t.Fatal("write deny-scanned harness recovery")
	}
	rejections := 0
	for _, c := range b.Children {
		rejections += len(c.RejectedRows)
	}
	t.Logf("shared harness executed/passed/failed/skipped=%d/%d/%d/%d children=%d rejections=%d complete_context=%t", b.XML.Executed, b.XML.Passed, b.XML.Failed, b.XML.Skipped, len(b.Children), rejections, b.ContextComplete)
	t.Fatal("shared diagnostic retained; return evidence to builder for exact rejecting-guard diagnosis before parser repair and focused proof")
}
