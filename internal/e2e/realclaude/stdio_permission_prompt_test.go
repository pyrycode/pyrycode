//go:build e2e_realclaude

package realclaude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

const (
	stdioPermissionModel       = "haiku"
	stdioPermissionWitness     = "STDIO_PERMISSION_WITNESS"
	stdioPermissionRequestWait = 2 * time.Minute
	stdioPermissionDenyMessage = "permission denied by the stdio compatibility test"
)

type stdioPermissionRequest struct {
	RequestID string
	Subtype   string
	ToolName  string
	ToolUseID string
}

type stdioPermissionDenial struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

type stdioPermissionResult struct {
	Subtype           string
	PermissionDenials []stdioPermissionDenial
}

type stdioPermissionReadings struct {
	DecodedLines    int
	MalformedLines  int
	PermissionAsks  int
	ResultLines     int
	ScanFailed      bool
	TypeCensus      map[string]int
	ControlSubtypes map[string]int
}

type stdioPermissionReader struct {
	requests chan stdioPermissionRequest
	results  chan stdioPermissionResult
	done     chan struct{}
	readings stdioPermissionReadings
}

func newStdioPermissionReader() *stdioPermissionReader {
	return &stdioPermissionReader{
		requests: make(chan stdioPermissionRequest, 2),
		results:  make(chan stdioPermissionResult, 1),
		done:     make(chan struct{}),
		readings: stdioPermissionReadings{
			TypeCensus:      make(map[string]int),
			ControlSubtypes: make(map[string]int),
		},
	}
}

func (r *stdioPermissionReader) read(scanner *bufio.Scanner) {
	defer close(r.done)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype   string `json:"subtype"`
				ToolName  string `json:"tool_name"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"request"`
			PermissionDenials []stdioPermissionDenial `json:"permission_denials"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			r.readings.MalformedLines++
			continue
		}
		r.readings.DecodedLines++
		r.readings.TypeCensus[stdioPermissionSafeLabel(line.Type)]++

		switch line.Type {
		case "control_request":
			subtype := stdioPermissionSafeLabel(line.Request.Subtype)
			r.readings.ControlSubtypes[subtype]++
			if line.Request.Subtype != "can_use_tool" {
				continue
			}
			r.readings.PermissionAsks++
			select {
			case r.requests <- stdioPermissionRequest{
				RequestID: line.RequestID,
				Subtype:   line.Request.Subtype,
				ToolName:  line.Request.ToolName,
				ToolUseID: line.Request.ToolUseID,
			}:
			default:
			}
		case "result":
			r.readings.ResultLines++
			select {
			case r.results <- stdioPermissionResult{
				Subtype:           line.Subtype,
				PermissionDenials: line.PermissionDenials,
			}:
			default:
			}
		}
	}
	r.readings.ScanFailed = scanner.Err() != nil
}

func stdioPermissionSafeLabel(s string) string {
	if s == "" {
		return "<empty>"
	}
	if len(s) > 64 {
		return "<invalid>"
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return "<invalid>"
	}
	return s
}

func stdioPermissionCensus(census map[string]int) string {
	keys := make([]string, 0, len(census))
	for key := range census {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, census[key]))
	}
	return strings.Join(parts, ",")
}

func stdioPermissionDiagnostics(version string, readings stdioPermissionReadings, exitCode int,
	deadline bool, stderrBytes int) string {
	return fmt.Sprintf("claude_version=%s decoded=%d malformed=%d types=[%s] "+
		"control_subtypes=[%s] can_use_tool=%d results=%d exit=%d deadline=%t "+
		"stdout_scan_failed=%t stderr_bytes=%d",
		stdioPermissionSafeLabel(version), readings.DecodedLines, readings.MalformedLines,
		stdioPermissionCensus(readings.TypeCensus),
		stdioPermissionCensus(readings.ControlSubtypes), readings.PermissionAsks,
		readings.ResultLines, exitCode, deadline, readings.ScanFailed, stderrBytes)
}

func stdioPermissionConfig(pyryBin, socketPath string) ([]byte, error) {
	type server struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	doc := struct {
		MCPServers map[string]server `json:"mcpServers"`
	}{MCPServers: map[string]server{
		"pyry_approve": {Command: pyryBin, Args: []string{"mcp-approve", "-pyry-socket", socketPath}},
		"pyry_files":   {Command: pyryBin, Args: []string{"mcp-files", "-pyry-socket", socketPath}},
	}}
	return json.Marshal(doc)
}

func stdioPermissionClaudeVersion(claudeBin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, claudeBin, "--version").Output()
	if err != nil {
		return "unavailable"
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "unavailable"
	}
	return fields[0]
}

func stdioPermissionDenied(result stdioPermissionResult, request stdioPermissionRequest) bool {
	for _, denial := range result.PermissionDenials {
		if denial.ToolName == request.ToolName && denial.ToolUseID == request.ToolUseID {
			return true
		}
	}
	return false
}

func TestRealClaude_StdioPermissionPromptDeny(t *testing.T) {
	claudeBin := resolveClaudeBin(t)
	workdir := WithWorktreeAuthenticated(t)
	pyryBin := ensurePyryBuilt(t)
	version := stdioPermissionClaudeVersion(claudeBin)

	systemPath := filepath.Join(t.TempDir(), "system.txt")
	if err := os.WriteFile(systemPath, []byte("Use only the tool explicitly requested by the user."), 0o600); err != nil {
		t.Fatal("#2342: could not write the test-authored system prompt")
	}
	configBytes, err := stdioPermissionConfig(pyryBin, filepath.Join(workdir, "pyry.sock"))
	if err != nil {
		t.Fatal("#2342: could not encode the production-shape MCP config")
	}
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal("#2342: could not write the production-shape MCP config")
	}

	argv := streamrunner.BuildClaudeArgs(streamrunner.ArgsParams{
		SystemPromptFile: systemPath,
		Model:            stdioPermissionModel,
		Effort:           "low",
		MaxTurns:         2,
		AllowedTools:     []string{"Read"},
		PermissionArgs: []string{
			"--permission-prompt-tool", "stdio",
			"--mcp-config", configPath,
			"--strict-mcp-config",
			"--permission-mode", "default",
		},
	})
	prompt := "Use the Bash tool once to run exactly `printf denied > " + stdioPermissionWitness +
		"`. Do not use another tool. After the command is accepted or refused, briefly report the outcome."
	turn, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": prompt}},
		},
	})
	if err != nil {
		t.Fatal("#2342: could not encode the test-authored turn")
	}

	ctx, cancel := context.WithTimeout(context.Background(), stdioPermissionRequestWait)
	cmd := exec.CommandContext(ctx, claudeBin, argv...)
	cmd.Dir = workdir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatalf("#2342: claude stdin pipe failed (claude_version=%s)", stdioPermissionSafeLabel(version))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("#2342: claude stdout pipe failed (claude_version=%s)", stdioPermissionSafeLabel(version))
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("#2342: claude start failed (claude_version=%s error_type=%T)",
			stdioPermissionSafeLabel(version), err)
	}

	reader := newStdioPermissionReader()
	go reader.read(bufio.NewScanner(stdout))

	var failure string
	if _, err := stdin.Write(append(turn, '\n')); err != nil {
		failure = "writing the user turn failed"
	}

	var request stdioPermissionRequest
	if failure == "" {
		select {
		case request = <-reader.requests:
		case <-reader.done:
			failure = "claude exited before emitting can_use_tool"
		case <-ctx.Done():
			failure = "claude timed out before emitting can_use_tool"
		}
	}
	if failure == "" {
		switch {
		case request.RequestID == "":
			failure = "can_use_tool carried an empty request id"
		case request.Subtype != "can_use_tool":
			failure = "the permission request had the wrong subtype"
		case request.ToolName != "Bash":
			failure = "can_use_tool named an unexpected tool"
		case request.ToolUseID == "":
			failure = "can_use_tool carried an empty tool-use id"
		}
	}
	witnessPath := filepath.Join(workdir, stdioPermissionWitness)
	if failure == "" {
		if _, err := os.Stat(witnessPath); err == nil {
			failure = "the side-effect witness existed before the permission response"
		} else if !errors.Is(err, os.ErrNotExist) {
			failure = "the pre-response witness check failed"
		}
	}
	if failure == "" {
		if err := streamsup.WriteCanUseToolDeny(stdin, request.RequestID,
			stdioPermissionDenyMessage, false); err != nil {
			failure = "writing the correlated deny control_response failed"
		}
	}

	var result stdioPermissionResult
	gotResult := false
	if failure == "" {
		select {
		case result = <-reader.results:
			gotResult = true
		case extra := <-reader.requests:
			if extra.RequestID != request.RequestID {
				failure = "claude emitted a second can_use_tool request instead of completing the turn"
			} else {
				failure = "claude repeated can_use_tool after the matching response"
			}
		case <-reader.done:
		case <-ctx.Done():
			failure = "claude stalled after the matching deny control_response"
		}
	}
	if !gotResult {
		select {
		case result = <-reader.results:
			gotResult = true
		default:
		}
	}

	_ = stdin.Close()
	cancel()
	waitErr := cmd.Wait()
	<-reader.done
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	diagnostics := stdioPermissionDiagnostics(version, reader.readings, exitCode,
		ctx.Err() == context.DeadlineExceeded, stderr.Len())

	if failure != "" {
		t.Fatalf("#2342: %s; %s", failure, diagnostics)
	}
	if waitErr != nil && !gotResult {
		t.Fatalf("#2342: claude exited without completing the turn (error_type=%T); %s", waitErr, diagnostics)
	}
	if !gotResult {
		t.Fatalf("#2342: no terminal result followed the matching deny control_response; %s", diagnostics)
	}
	if result.Subtype != "success" {
		t.Fatalf("#2342: terminal result subtype=%s, want success after refusal; %s",
			stdioPermissionSafeLabel(result.Subtype), diagnostics)
	}
	if !stdioPermissionDenied(result, request) {
		t.Fatalf("#2342: terminal result did not correlate a Bash permission denial to the observed tool-use id; %s",
			diagnostics)
	}
	if _, err := os.Stat(witnessPath); err == nil {
		t.Fatalf("#2342: the denied command executed and created the side-effect witness; %s", diagnostics)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("#2342: the final witness check failed; %s", diagnostics)
	}
}

func TestStdioPermissionPromptHelpers(t *testing.T) {
	if got := stdioPermissionSafeLabel("sub/type"); got != "<invalid>" {
		t.Fatalf("stdioPermissionSafeLabel(hostile) = %q, want <invalid>", got)
	}
	result := stdioPermissionResult{PermissionDenials: []stdioPermissionDenial{
		{ToolName: "Bash", ToolUseID: "toolu-other"},
		{ToolName: "Bash", ToolUseID: "toolu-want"},
	}}
	if !stdioPermissionDenied(result, stdioPermissionRequest{ToolName: "Bash", ToolUseID: "toolu-want"}) {
		t.Fatal("stdioPermissionDenied did not match the request's tool name and tool-use id")
	}
}

func TestStdioPermissionPromptReader(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"control_request","request_id":"request-1","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu-1","input":{"command":"secret"}}}`,
		`{"type":"result","subtype":"success","permission_denials":[{"tool_name":"Bash","tool_use_id":"toolu-1"}]}`,
	}, "\n")
	reader := newStdioPermissionReader()
	reader.read(bufio.NewScanner(strings.NewReader(input)))

	request := <-reader.requests
	if request.RequestID != "request-1" || request.Subtype != "can_use_tool" ||
		request.ToolName != "Bash" || request.ToolUseID != "toolu-1" {
		t.Fatalf("decoded request = %+v, want correlated Bash request", request)
	}
	result := <-reader.results
	if result.Subtype != "success" || !stdioPermissionDenied(result, request) {
		t.Fatalf("decoded result = %+v, want correlated successful denial", result)
	}
	if reader.readings.DecodedLines != 3 || reader.readings.PermissionAsks != 1 ||
		reader.readings.ResultLines != 1 || reader.readings.ControlSubtypes["can_use_tool"] != 1 {
		t.Fatalf("reader counts = %+v, want 3 decoded, 1 ask, 1 result", reader.readings)
	}
}
