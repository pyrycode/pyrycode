//go:build e2e_realclaude

package realclaude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
)

// Only selected source fields cross this test-only socket. They stay in memory,
// never enter diagnostic logs, and are observed before the daemon reads stdout.
type permissionObservation struct {
	Type           string   `json:"type"`
	Subtype        string   `json:"subtype,omitempty"`
	Tools          []string `json:"tools,omitempty"`
	PermissionMode string   `json:"permissionMode,omitempty"`
	RequestID      string   `json:"request_id"`
	Request        struct {
		Subtype   string `json:"subtype"`
		ToolName  string `json:"tool_name"`
		ToolUseID string `json:"tool_use_id"`
		Input     struct {
			FilePath string `json:"file_path"`
			Command  string `json:"command"`
		} `json:"input"`
		Reason                  json.RawMessage `json:"decision_reason,omitempty"`
		ReasonType              json.RawMessage `json:"decision_reason_type,omitempty"`
		BlockedPath             string          `json:"blocked_path"`
		Description             string          `json:"description"`
		DefaultToNo             bool            `json:"default_to_no"`
		PermissionSuggestions   json.RawMessage `json:"permission_suggestions,omitempty"`
		SuppressAlwaysAllowRule bool            `json:"suppress_always_allow_rule"`
	} `json:"request"`
	PermissionDenials []struct {
		ToolUseID string `json:"tool_use_id"`
	} `json:"permission_denials"`
	// Present only on a control_response, and only while the posture arm is armed
	// (#2474). A POINTER UNDER omitempty is load-bearing rather than stylistic:
	// runPermissionObserver re-encodes the WHOLE observation over its socket, and both
	// TestPermissionObservation_PreservesAlwaysAllowSource and
	// TestPermissionObservation_PreservesReasonPresence round-trip this type through
	// json.Marshal and assert presence semantics over the result. A value field would
	// add a response key to every one of those paths' bytes. Nil omits.
	//
	// It is NOT writePermissionOfferDiagnostic that constrains this, though that is the
	// nearby function a reader reaches for: it marshals permissionOfferDiagnostic, a
	// separate record it builds from selected fields of an observation, so nothing on
	// this type reaches a published artifact. Said explicitly because the wrong citation
	// stood here first, and on a shared type in a security-sensitive change it would
	// point a future exposure audit at bytes that are never written.
	Response *permissionControlResponse `json:"response,omitempty"`
}

// permissionControlResponse carries the two fields that identify claude's SUCCESS
// answer to the spawn-time set_permission_mode request, and nothing else.
//
// THE NAK's `error` IS DELIBERATELY ABSENT. A non-success subtype already identifies a
// NAK, and the error string is unbounded claude prose that would otherwise cross this
// socket into a test log — the channel noteControlAck refuses to open for exactly this
// reason. A field never declared cannot reach a log. Both retained fields are
// claude-authored and pass through stdioPermissionSafeLabel before any comparison or
// log; see isDefaultPostureAck, which is their only reader.
type permissionControlResponse struct {
	Subtype  string `json:"subtype"`
	Response struct {
		Mode string `json:"mode"`
	} `json:"response"`
}

// Invoked by TestMain only when this test binary stands in front of real Claude.
// It passes stdin and stdout unchanged; it never manufactures a control message.
func runPermissionObserver() int {
	realBin := os.Getenv("PYRY_PERMISSION_OBSERVER_REAL")
	if realBin == "" || realBin == os.Args[0] {
		return 91
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cmd := exec.CommandContext(ctx, realBin, os.Args[1:]...)
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 92
	}
	if err = cmd.Start(); err != nil {
		return 93
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Read once, outside the loop: the arm is fixed for the process's life. Off by
	// default, so the two existing observer callers forward exactly what they always
	// did — startPermissionObserver's channel holds 16 and its accept loop returns
	// permanently once full, so an unconditional third arm could silently end their
	// observation (#2474).
	posture := os.Getenv("PYRY_PERMISSION_OBSERVER_POSTURE") == "1"
	questions := os.Getenv("PYRY_PERMISSION_OBSERVER_QUESTIONS") == "1"
	reader := bufio.NewReader(stdout)
	for {
		line, readErr := reader.ReadBytes('\n')
		var observed permissionObservation
		if json.Unmarshal(line, &observed) == nil &&
			((observed.Type == "control_request" && observed.Request.Subtype == "can_use_tool") ||
				observed.Type == "result" ||
				(posture && observed.Type == "control_response") ||
				(questions && observed.Type == "system" && observed.Subtype == "init")) {
			conn, err := net.DialTimeout("tcp", os.Getenv("PYRY_PERMISSION_OBSERVER_ADDR"), 5*time.Second)
			if err != nil {
				return 94
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err = json.NewEncoder(conn).Encode(observed)
			_ = conn.Close()
			if err != nil {
				return 95
			}
		}
		if len(line) > 0 {
			if _, err := os.Stdout.Write(line); err != nil {
				return 96
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return 97
			}
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return 98
	}
	return 0
}

func startPermissionObserver(t *testing.T) (<-chan permissionObservation, func(string) string, func() string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("permission observer listen failed")
	}
	observations := make(chan permissionObservation, 16)
	observerDone := make(chan struct{})
	go func() {
		defer close(observerDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var observed permissionObservation
			err = json.NewDecoder(io.LimitReader(conn, 2<<20)).Decode(&observed)
			_ = conn.Close()
			if err == nil {
				select {
				case observations <- observed:
				default:
					return
				}
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-observerDone })

	var version string
	configure := func(realBin string) string {
		version = stdioPermissionSafeLabel(stdioPermissionClaudeVersion(realBin))
		t.Logf("claude_version=%s", version)
		t.Setenv("PYRY_PERMISSION_OBSERVER", "1")
		t.Setenv("PYRY_PERMISSION_OBSERVER_REAL", realBin)
		t.Setenv("PYRY_PERMISSION_OBSERVER_ADDR", listener.Addr().String())
		return os.Args[0]
	}
	return observations, configure, func() string { return version }
}

// Claude 2.1.259 supplied no reason for the ordinary Bash write and supplied
// workingDir plus text for the outside-directory Write. Both are mandatory source
// preconditions: upstream drift must not masquerade as a successful forwarding test.
func TestInteractiveStreamStdioModalResolution(t *testing.T) {
	for _, outside := range []bool{false, true} {
		name := "absent"
		if outside {
			name = "working_directory_reason"
		}
		t.Run(name, func(t *testing.T) {
			observations, configure, _ := startPermissionObserver(t)
			h, convID, reconnect := startObservedPermissionHarness(t, permissionDaemonModel, true, configure)
			targetDir := h.workdir
			if outside {
				targetDir = t.TempDir()
			}
			target := filepath.Join(targetDir, "permission-context-witness.txt")
			command := "printf hello > permission-context-witness.txt"
			prompt := "Use the Bash tool once to run exactly `" + command + "`. Then briefly report the outcome."
			if outside {
				prompt = fmt.Sprintf("Use the Write tool to create a file containing exactly the word hello at %s. Then briefly report the outcome.", target)
			}
			scenario.PermissionContext(t, sharedHarnessWire(t, h, 2), scenario.PermissionContextConfig{ConversationID: convID, Target: target, Prompt: prompt, Outside: outside, SurfaceBudget: modalSurfaceBudget, TurnBudget: perTurnReplyBudget, Reconnect: reconnect, RequireAbsent: func() { requirePermissionWitnessAbsent(t, target) }, Source: func(kind string) scenario.PermissionContextSource {
				raw, err := json.Marshal(nextPermissionObservation(t, observations, kind))
				if err != nil {
					t.Fatal("native permission observation encoding failed")
				}
				var source scenario.PermissionContextSource
				if json.Unmarshal(raw, &source) != nil {
					t.Fatal("native permission observation decoding failed")
				}
				return source
			}})

		})
	}
}

func nextPermissionObservation(t *testing.T, observations <-chan permissionObservation, kind string) permissionObservation {
	t.Helper()
	timer := time.NewTimer(modalSurfaceBudget)
	defer timer.Stop()
	for {
		select {
		case observed := <-observations:
			if observed.Type == kind {
				return observed
			}
		case <-timer.C:
			t.Fatal("source observation missing; not a forwarding pass")
		}
	}
}

func requirePermissionWitnessAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("test-owned write occurred or absence could not be verified")
	}
}

func requirePermissionContext(t *testing.T, source permissionObservation, raw json.RawMessage) {
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var selected scenario.PermissionContextSource
	if json.Unmarshal(encoded, &selected) != nil {
		t.Fatal("invalid selected permission source")
	}
	scenario.RequirePermissionContext(t, selected, raw)
}

// The observer re-encodes selected fields over its private socket. Omitting an
// absent RawMessage must not turn it into a present JSON null at that boundary.
func TestPermissionObservation_PreservesReasonPresence(t *testing.T) {
	for _, suffix := range []string{
		"",
		`,"decision_reason":null`,
		`,"decision_reason":"outside directory"`,
		`,"decision_reason_type":null`,
		`,"decision_reason_type":""`,
		`,"decision_reason_type":"workingDir"`,
	} {
		var before, after permissionObservation
		if err := json.Unmarshal([]byte(`{"request":{"subtype":"can_use_tool"`+suffix+`}}`), &before); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Request.Reason, after.Request.Reason) {
			t.Fatal("observer changed source reason presence or value")
		}
		if !reflect.DeepEqual(before.Request.ReasonType, after.Request.ReasonType) {
			t.Fatal("observer changed source reason type presence or value")
		}
	}
}
