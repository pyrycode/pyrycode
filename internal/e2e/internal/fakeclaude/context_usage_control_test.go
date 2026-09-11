package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const contextUsageCapturePath = "../../realclaude/testdata/context_usage_v2.1.259.json"

func contextUsageRequestLine(requestID, detail string) string {
	return fmt.Sprintf(`{"type":"control_request","request_id":%q,"request":{"subtype":"get_context_usage","detail":%q}}`,
		requestID, detail)
}

type contextUsageAck struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Model                string           `json:"model"`
			TotalTokens          int              `json:"totalTokens"`
			MaxTokens            int              `json:"maxTokens"`
			RawMaxTokens         int              `json:"rawMaxTokens"`
			AutocompactSource    string           `json:"autocompactSource"`
			Percentage           int              `json:"percentage"`
			AutoCompactThreshold int              `json:"autoCompactThreshold"`
			IsAutoCompactEnabled bool             `json:"isAutoCompactEnabled"`
			Categories           []map[string]any `json:"categories"`
			MCPTools             []map[string]any `json:"mcpTools"`
			MemoryFiles          []map[string]any `json:"memoryFiles"`
		} `json:"response"`
	} `json:"response"`
}

func answerContextUsage(t *testing.T, requestID, detail string, honorInterrupt bool) (contextUsageAck, []byte) {
	t.Helper()

	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(contextUsageRequestLine(requestID, detail)+"\n"), &buf,
		honorInterrupt, false, "", false, 0, false, "", false)

	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatalf("detail=%q honorInterrupt=%v: get_context_usage went unanswered", detail, honorInterrupt)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("detail=%q honorInterrupt=%v: got %d response lines, want exactly 1:\n%s",
			detail, honorInterrupt, len(lines), out)
	}

	var ack contextUsageAck
	if err := json.Unmarshal([]byte(lines[0]), &ack); err != nil {
		t.Fatalf("unmarshal get_context_usage response: %v\n%s", err, lines[0])
	}
	payload, err := json.Marshal(ack.Response.Response)
	if err != nil {
		t.Fatalf("marshal decoded context-usage payload: %v", err)
	}
	return ack, payload
}

func TestRunStreamJSON_ContextUsageAnswer(t *testing.T) {
	t.Parallel()

	var reference []byte
	for _, tc := range []struct {
		name           string
		detail         string
		honorInterrupt bool
	}{
		{name: "summary default mode", detail: "summary"},
		{name: "summary interrupt mode", detail: "summary", honorInterrupt: true},
		{name: "full default mode", detail: "full"},
		{name: "full interrupt mode", detail: "full", honorInterrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "e2e-2289-" + strings.ReplaceAll(tc.name, " ", "-")
			ack, payload := answerContextUsage(t, requestID, tc.detail, tc.honorInterrupt)

			if ack.Type != "control_response" {
				t.Errorf("type = %q, want control_response", ack.Type)
			}
			if ack.Subtype != "" {
				t.Errorf("top-level subtype = %q, want empty", ack.Subtype)
			}
			if ack.Response.Subtype != "success" {
				t.Errorf("response.subtype = %q, want success", ack.Response.Subtype)
			}
			if ack.Response.RequestID != requestID {
				t.Errorf("response.request_id = %q, want echoed id %q", ack.Response.RequestID, requestID)
			}
			if len(ack.Response.Response.Categories) == 0 || len(ack.Response.Response.MCPTools) == 0 ||
				len(ack.Response.Response.MemoryFiles) == 0 {
				t.Errorf("nested lists have lengths categories=%d mcpTools=%d memoryFiles=%d; want all non-empty",
					len(ack.Response.Response.Categories), len(ack.Response.Response.MCPTools),
					len(ack.Response.Response.MemoryFiles))
			}

			if reference == nil {
				reference = append([]byte(nil), payload...)
			} else if !bytes.Equal(payload, reference) {
				t.Errorf("detail=%q honorInterrupt=%v payload differs from first row\nfirst: %s\nthis:  %s",
					tc.detail, tc.honorInterrupt, reference, payload)
			}
		})
	}
}

func TestContextUsageCannedPayloadMatchesCapturedVocabulary(t *testing.T) {
	t.Parallel()

	ack, _ := answerContextUsage(t, "e2e-2289-vocabulary", "summary", false)
	payload := ack.Response.Response

	if payload.Model == "" {
		t.Error("response.response.model is empty")
	}
	if payload.TotalTokens <= 0 || payload.MaxTokens <= 0 || payload.RawMaxTokens <= 0 {
		t.Errorf("token totals = (%d, %d, %d), want positive values",
			payload.TotalTokens, payload.MaxTokens, payload.RawMaxTokens)
	}
	if payload.AutocompactSource == "" {
		t.Error("response.response.autocompactSource is empty")
	}
	if payload.Percentage <= 0 || payload.Percentage >= 100 {
		t.Errorf("response.response.percentage = %d, want a non-trivial percentage", payload.Percentage)
	}
	if payload.AutoCompactThreshold <= 0 {
		t.Errorf("response.response.autoCompactThreshold = %d, want a positive threshold",
			payload.AutoCompactThreshold)
	}
	if !payload.IsAutoCompactEnabled {
		t.Error("response.response.isAutoCompactEnabled = false, want captured true reading")
	}

	requiredPayloadKeys := []string{
		"model", "totalTokens", "maxTokens", "rawMaxTokens", "autocompactSource", "percentage",
		"autoCompactThreshold", "isAutoCompactEnabled",
		"categories", "mcpTools", "memoryFiles",
	}
	capturedPayload := readCapturedContextUsagePayload(t)
	for _, key := range requiredPayloadKeys {
		if _, ok := capturedPayload[key]; !ok {
			t.Errorf("required canned key %q does not occur in the captured inner payload", key)
		}
	}

	assertEntryKeySets(t, "categories", payload.Categories, map[string]bool{
		"color,name,tokens":            true,
		"color,isDeferred,name,tokens": true,
	})
	assertEntryKeySets(t, "mcpTools", payload.MCPTools, map[string]bool{
		"isLoaded,name,serverName,tokens": true,
	})
	assertEntryKeySets(t, "memoryFiles", payload.MemoryFiles, map[string]bool{
		"path,tokens": true,
	})
}

func readCapturedContextUsagePayload(t *testing.T) map[string]any {
	t.Helper()

	b, err := os.ReadFile(contextUsageCapturePath)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(contextUsageCapturePath), err)
	}
	var capture struct {
		Arms []struct {
			ControlResponses []struct {
				Response struct {
					Response map[string]any `json:"response"`
				} `json:"response"`
			} `json:"control_responses"`
		} `json:"arms"`
	}
	if err := json.Unmarshal(b, &capture); err != nil {
		t.Fatalf("unmarshal %s: %v", filepath.Base(contextUsageCapturePath), err)
	}
	if len(capture.Arms) == 0 || len(capture.Arms[0].ControlResponses) == 0 {
		t.Fatalf("%s has no first-arm response to attest canned vocabulary",
			filepath.Base(contextUsageCapturePath))
	}
	return capture.Arms[0].ControlResponses[0].Response.Response
}

func assertEntryKeySets(t *testing.T, list string, entries []map[string]any, allowed map[string]bool) {
	t.Helper()

	if len(entries) == 0 {
		t.Fatalf("%s is empty", list)
	}
	for i, entry := range entries {
		keys := make([]string, 0, len(entry))
		for key := range entry {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		set := strings.Join(keys, ",")
		if !allowed[set] {
			t.Errorf("%s[%d] key set = %q, want one of %v", list, i, set, reflect.ValueOf(allowed).MapKeys())
		}
	}
}

func TestContextUsageCannedPayloadStressesConsumerBounds(t *testing.T) {
	t.Parallel()

	ack, _ := answerContextUsage(t, "e2e-2289-bounds", "full", false)
	payload := ack.Response.Response

	if len(payload.MCPTools) <= 32 {
		t.Errorf("mcpTools length = %d, want more than 32", len(payload.MCPTools))
	}

	longEntry := false
	for _, tool := range payload.MCPTools {
		name, _ := tool["name"].(string)
		if len(name) > 256 {
			longEntry = true
		}
	}
	for _, memoryFile := range payload.MemoryFiles {
		path, _ := memoryFile["path"].(string)
		if len(path) > 256 {
			longEntry = true
		}
	}
	if !longEntry {
		t.Error("no canned MCP tool name or memory-file path exceeds 256 bytes")
	}

	for name, entries := range map[string][]map[string]any{
		"categories":  payload.Categories,
		"mcpTools":    payload.MCPTools,
		"memoryFiles": payload.MemoryFiles,
	} {
		if sort.SliceIsSorted(entries, func(i, j int) bool {
			return jsonInt(entries[i]["tokens"]) > jsonInt(entries[j]["tokens"])
		}) {
			t.Errorf("%s token counts are already in descending order; want sorting work before cutting", name)
		}
	}
}

func jsonInt(value any) int {
	n, _ := value.(float64)
	return int(n)
}

func TestContextUsageCannedPathsAreFictional(t *testing.T) {
	t.Parallel()

	ack, _ := answerContextUsage(t, "e2e-2289-paths", "summary", false)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve operator home: %v", err)
	}

	for i, memoryFile := range ack.Response.Response.MemoryFiles {
		path, ok := memoryFile["path"].(string)
		if !ok || path == "" {
			t.Errorf("memoryFiles[%d].path = %T(%v), want a non-empty string", i, memoryFile["path"], memoryFile["path"])
			continue
		}
		if !strings.HasPrefix(path, "/__pyry_fake__/memory/") {
			t.Errorf("memoryFiles[%d].path = %q, want the fictional /__pyry_fake__/memory/ root", i, path)
		}
		if pathWithin(path, home) {
			t.Errorf("memoryFiles[%d].path is rooted under the executing operator home", i)
		}
	}
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestRunStreamJSON_ContextUsageRejectsMalformedAndUnknownRequests(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		line string
	}{
		{name: "malformed JSON", line: `{"type":"control_request"`},
		{name: "unknown subtype", line: `{"type":"control_request","request_id":"e2e-2289-unknown","request":{"subtype":"not_context_usage"}}`},
		{name: "wrong envelope type", line: `{"type":"control_response","request_id":"e2e-2289-wrong","request":{"subtype":"get_context_usage"}}`},
		{name: "missing detail", line: `{"type":"control_request","request_id":"e2e-2289-no-detail","request":{"subtype":"get_context_usage"}}`},
		{name: "unknown detail", line: `{"type":"control_request","request_id":"e2e-2289-bad-detail","request":{"subtype":"get_context_usage","detail":"everything"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			runStreamJSON(strings.NewReader(tc.line+"\n"), &buf,
				false, false, "", false, 0, false, "", false)
			if buf.Len() != 0 {
				t.Fatalf("request produced %d bytes, want no answer: %q", buf.Len(), buf.String())
			}
		})
	}
}
