//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type readinessWitness struct {
	At       time.Time `json:"at"`
	Boundary string    `json:"boundary"`
	Ordinal  int       `json:"ordinal"`
	OK       bool      `json:"ok"`
	Subject  string    `json:"-"`
	Value    int       `json:"-"`
}

type readinessTurn struct {
	Ordinal                                                                 int
	Created, Sent, Enqueued, Delivered, Opened                              time.Time
	PromptMatches, ReplyMatches, ExactlyOnce, ConfirmedBeforeDelivery, Idle bool
	AdmissionWithin90Seconds                                                bool
}

type readinessCapture struct {
	Test, Arm, DaemonCommit, ClaudeVersion, Conclusion string
	Dirty                                              bool
	Setup                                              map[string]any
	Counts                                             map[string]int
	Turns                                              []readinessTurn
	Witnesses                                          []readinessWitness
	Resources                                          []map[string]any
}

// TestInteractiveStreamQueuedTurnReadiness compares retained completed turns
// with a fresh daemon. A deadline miss stays a failure during continued capture.
func TestInteractiveStreamQueuedTurnReadiness(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pyry")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-tags", "e2e_realclaude", "-o", bin, "github.com/pyrycode/pyrycode/cmd/pyry")
	build.Env = buildEnvWithRealHome()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("instrumented daemon build failed: %v\n%s", err, out)
	}
	commit, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal("daemon revision unavailable")
	}
	dirty, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		t.Fatal("source state unavailable")
	}
	version, err := exec.Command("claude", "--version").Output()
	if err != nil {
		t.Fatal("Claude version unavailable")
	}
	versionToken := regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+\b`).FindString(string(version))
	if versionToken == "" {
		t.Fatal("Claude version unrecognized")
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, arm := range []struct {
		name      string
		preceding int
	}{{"retained", 32}, {"fresh", 0}} {
		r := &readinessCapture{Test: t.Name() + "/" + arm.name, Arm: arm.name, DaemonCommit: strings.TrimSpace(string(commit)), ClaudeVersion: versionToken, Dirty: len(dirty) > 0, Conclusion: "diagnostic incomplete; cause unknown", Setup: map[string]any{
			"model": "sonnet", "stored_permission_mode": "default", "operator_bypass": false, "daemon_composed_bypass": true,
			"idle_timeout_seconds": 0, "active_child_cap": 0, "sweep_interval_seconds": 3600, "conversation_auto_archive_days": 30,
			"preceding_turns": arm.preceding, "pacing": "serial completed turn then idle", "retention": "same daemon, no removals",
			"enableAllProjectMcpServers": true, "skipDangerousModePermissionPrompt": true,
			"original_idle_cap_pacing": "unknown", "reproduction_assumptions": true, "stdout_boundary": "complete line available to forwarder; kernel arrival unknown",
		}}
		passed := t.Run(arm.name, func(t *testing.T) { readinessRunArm(t, bin, arm.preceding, r) })
		r.Counts = map[string]int{"executed": 1, "passed": 0, "failed": 0, "skipped": 0}
		if passed {
			r.Counts["passed"] = 1
		} else {
			r.Counts["failed"] = 1
		}
		dir := filepath.Join("testdata", "queued_turn_readiness")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal("evidence directory unavailable")
		}
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatal("evidence encoding failed")
		}
		path := filepath.Join(dir, stamp+"-"+arm.name+".json")
		if err := os.WriteFile(path+".tmp", append(b, '\n'), 0o600); err != nil {
			t.Fatal("evidence write failed")
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Fatal("evidence publish failed")
		}
		t.Logf("portable evidence: %s; executed=1 passed=%d failed=%d skipped=0", path, r.Counts["passed"], r.Counts["failed"])
	}
}

func readinessRunArm(t *testing.T, bin string, preceding int, r *readinessCapture) {
	private := filepath.Join(t.TempDir(), "observations.jsonl")
	t.Setenv("PYRY_E2E_READINESS_BIN", bin)
	t.Setenv("PYRY_E2E_READINESS_RECORD", private)
	h, _, _ := startObservedPermissionHarnessWithOperatorBypass(t, "sonnet", true, nil, false)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			r.Resources = append(r.Resources, readinessResources(ctx, h.daemon.cmd.Process.Pid, private))
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	t.Cleanup(func() { stop(); <-done })
	subjects := map[string]int{streamModalBootstrapUUID: 0}
	t.Cleanup(func() { r.Witnesses = readinessReadObservations(private, subjects) })
	for i := 1; i <= preceding+1; i++ {
		created := time.Now().UTC()
		conv := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, uint64(100+i*2), nil)
		reg, err := conversations.Load(filepath.Join(h.home, ".pyry", "test", "conversations.json"))
		if err != nil {
			t.Fatal("conversation binding unavailable")
		}
		c, ok := reg.Get(conversations.ConversationID(conv))
		if !ok || c.CurrentSessionID == "" {
			t.Fatal("fresh conversation lacks session binding")
		}
		subjects[conv], subjects[c.CurrentSessionID] = i, i
		readinessDefaultPosture(t, h.home, c.CurrentSessionID)
		turn := readinessTurn{Ordinal: i, Created: created, Sent: time.Now().UTC()}
		r.Turns = append(r.Turns, turn)
		got := &r.Turns[len(r.Turns)-1]
		prompt := "Reply with exactly ping. Do not use tools."
		msg := strconv.Itoa(i)
		sealSendMessage(t, h.phone, h.initSend, uint64(101+i*2), conv, msg, prompt)
		// The extra 120 seconds only collects a failed admission's later boundaries.
		until := time.Now().Add(210 * time.Second)
		ended := false
		busySeen, deadlineReported := false, false
		for time.Now().Before(until) {
			entries := readinessHistory(h.home, conv)
			readinessFoldTurn(got, entries, conv, msg, prompt)
			if !deadlineReported && !got.Enqueued.IsZero() && time.Since(got.Enqueued) > 90*time.Second && (got.Delivered.IsZero() || got.Opened.IsZero()) {
				t.Errorf("ordinal %d missed unchanged 90-second enqueue admission deadline", i)
				deadlineReported = true
			}
			if ended && got.Idle && got.ExactlyOnce && got.ReplyMatches {
				break
			}
			raw, err := h.phone.ReceiveBytes(250 * time.Millisecond)
			if err != nil {
				continue
			}
			var frame protocol.InnerFrameV2
			if json.Unmarshal(raw, &frame) != nil || frame.Type != protocol.TypeNoiseMsg {
				continue
			}
			cipher, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				t.Fatal("invalid encrypted frame")
			}
			plain, err := h.initRecv.Decrypt(cipher)
			if err != nil {
				t.Fatal("receive nonce desynchronized")
			}
			var env protocol.Envelope
			if json.Unmarshal(plain, &env) != nil {
				t.Fatal("invalid envelope")
			}
			var p struct {
				ConversationID string `json:"conversation_id"`
				State          string `json:"state"`
			}
			if json.Unmarshal(env.Payload, &p) != nil || p.ConversationID != conv {
				continue
			}
			if env.Type == protocol.TypeTurnEnd {
				ended = true
			}
			if env.Type == protocol.TypeTurnState && p.State == "busy" {
				busySeen, got.Idle = true, false
			}
			if busySeen && env.Type == protocol.TypeTurnState && p.State == "idle" {
				got.Idle = true
			}
		}
		got.AdmissionWithin90Seconds = !got.Enqueued.IsZero() && !got.Delivered.IsZero() && !got.Opened.IsZero() && got.Delivered.Sub(got.Enqueued) <= 90*time.Second && got.Opened.Sub(got.Enqueued) <= 90*time.Second
		obs := readinessReadObservations(private, subjects)
		boundaries := map[string]time.Time{}
		for _, o := range obs {
			if o.Ordinal == i && o.OK {
				boundaries[o.Boundary] = o.At
			}
		}
		got.ConfirmedBeforeDelivery = !boundaries["gate_released"].IsZero() && boundaries["gate_released"].Before(got.Delivered)
		for _, name := range []string{"child_started", "posture_armed", "permission_write", "ack_stdout_line", "ack_processing", "gate_released"} {
			if boundaries[name].IsZero() {
				t.Errorf("ordinal %d missing %s observation", i, name)
			}
		}
		if !got.AdmissionWithin90Seconds || !got.ExactlyOnce || !got.PromptMatches || !got.ReplyMatches || !got.ConfirmedBeforeDelivery || !got.Idle {
			t.Errorf("ordinal %d admission=%t once=%t prompt=%t reply=%t confirmed=%t idle=%t", i, got.AdmissionWithin90Seconds, got.ExactlyOnce, got.PromptMatches, got.ReplyMatches, got.ConfirmedBeforeDelivery, got.Idle)
			return
		}
		t.Logf("ordinal %d admitted in %s, turn completed and idle", i, got.Opened.Sub(got.Enqueued))
	}
	r.Conclusion = "non-reproduction in this arm; no host cause or repair established"
}

func readinessDefaultPosture(t *testing.T, home, session string) {
	t.Helper()
	var reg struct {
		Sessions []struct {
			ID         string `json:"id"`
			YOLO       bool   `json:"yolo"`
			Permission string `json:"permission_mode"`
		} `json:"sessions"`
	}
	b, err := os.ReadFile(filepath.Join(home, ".pyry", "test", "sessions.json"))
	if err != nil || json.Unmarshal(b, &reg) != nil {
		t.Fatal("stored posture unavailable")
	}
	found := false
	for _, s := range reg.Sessions {
		if s.ID == session {
			found = true
			if s.YOLO || (s.Permission != "" && s.Permission != "default") {
				t.Fatal("target is not stored default posture")
			}
		}
	}
	if !found {
		t.Fatal("stored session unavailable")
	}
	var settings struct {
		MCP  bool `json:"enableAllProjectMcpServers"`
		Skip bool `json:"skipDangerousModePermissionPrompt"`
	}
	b, err = os.ReadFile(filepath.Join(home, ".pyry", "test", "session-settings", session+".json"))
	if err != nil || json.Unmarshal(b, &settings) != nil || !settings.MCP || !settings.Skip {
		t.Fatal("recorded session MCP settings not preserved")
	}
}

func readinessHistory(home, conv string) []history.Entry {
	paths, _ := filepath.Glob(filepath.Join(home, ".pyry", "test", "conversations", conv, "history", "segment-*.jsonl"))
	var entries []history.Entry
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil || len(b) > 1<<20 {
			continue
		}
		for _, line := range bytes.Split(b, []byte{'\n'}) {
			var e history.Entry
			if json.Unmarshal(line, &e) == nil && e.ID > 0 {
				entries = append(entries, e)
			}
		}
	}
	return entries
}

func readinessFoldTurn(r *readinessTurn, entries []history.Entry, conv, msg, prompt string) {
	var acceptedID, messageID uint64
	var deliveries, messages, openings int
	var reply strings.Builder
	for _, e := range entries {
		var p struct {
			ConversationID  string    `json:"conversation_id"`
			MessageID       string    `json:"message_id"`
			Text            string    `json:"text"`
			Role            string    `json:"role"`
			AcceptedAt      time.Time `json:"accepted_at"`
			AcceptedEntryID uint64    `json:"accepted_entry_id"`
			DeliveryEntryID uint64    `json:"delivery_entry_id"`
		}
		if json.Unmarshal(e.Payload, &p) != nil || p.ConversationID != conv {
			continue
		}
		switch e.Type {
		case "send_accepted":
			if p.MessageID == msg {
				acceptedID = e.ID
				r.Enqueued = p.AcceptedAt
			}
		case "message":
			if p.MessageID == msg && p.Role == "user" {
				messages++
				messageID = e.ID
				r.PromptMatches = p.Text == prompt
			}
		case "send_delivered":
			if p.AcceptedEntryID == acceptedID && p.DeliveryEntryID == messageID {
				deliveries++
				r.Delivered = e.TS
			}
		case "main_turn_opened":
			openings++
			r.Opened = e.TS
		case "assistant_delta":
			reply.WriteString(p.Text)
		}
	}
	r.ExactlyOnce = messages == 1 && deliveries == 1 && openings == 1
	r.ReplyMatches = strings.EqualFold(strings.TrimSpace(reply.String()), "ping")
}

func readinessReadObservations(path string, subjects map[string]int) []readinessWitness {
	b, _ := os.ReadFile(path)
	var out []readinessWitness
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		var o struct {
			At       time.Time `json:"at"`
			Boundary string    `json:"boundary"`
			Subject  string    `json:"subject"`
			OK       bool      `json:"ok"`
			Value    int       `json:"value"`
		}
		if json.Unmarshal(line, &o) != nil {
			continue
		}
		ordinal, ok := subjects[o.Subject]
		if !ok {
			continue
		}
		out = append(out, readinessWitness{At: o.At, Boundary: o.Boundary, Ordinal: ordinal, OK: o.OK, Subject: o.Subject, Value: o.Value})
	}
	return out
}

func readinessResources(ctx context.Context, daemonPID int, path string) map[string]any {
	r := map[string]any{"at": time.Now().UTC(), "known": false}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(call, "ps", "-e", "-o", "pid=,ppid=,pcpu=,rss=").Output()
	if err != nil {
		return r
	}
	rows := map[int][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 4 {
			pid, _ := strconv.Atoi(f[0])
			rows[pid] = f
		}
	}
	selected := map[int]bool{daemonPID: true}
	for changed := true; changed; {
		changed = false
		for pid, f := range rows {
			parent, _ := strconv.Atoi(f[1])
			if selected[parent] && !selected[pid] {
				selected[pid] = true
				changed = true
			}
		}
	}
	var rss, processes, children int
	var cpu float64
	for pid := range selected {
		if f, ok := rows[pid]; ok {
			processes++
			n, _ := strconv.Atoi(f[3])
			rss += n
			c, _ := strconv.ParseFloat(f[2], 64)
			cpu += c
		}
	}
	b, _ := os.ReadFile(path)
	seen := map[int]bool{}
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		var o struct {
			Boundary string `json:"boundary"`
			Value    int    `json:"value"`
		}
		if json.Unmarshal(line, &o) == nil && o.Boundary == "child_started" && selected[o.Value] && rows[o.Value] != nil {
			seen[o.Value] = true
		}
	}
	children = len(seen)
	r["known"], r["processes"], r["live_claude_children"], r["descendant_rss_kib"], r["ps_lifetime_cpu_percent"] = true, processes, children, rss, cpu
	return r
}
