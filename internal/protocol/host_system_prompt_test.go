package protocol

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestHostSystemPromptPayloads_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		typ     string
		replyID uint64
		want    any
	}{
		{"request_host_system_prompt.json", TypeRequestHostSystemPrompt, 0, &RequestHostSystemPromptPayload{}},
		{"set_host_system_prompt.json", TypeSetHostSystemPrompt, 0, &SetHostSystemPromptPayload{SystemPrompt: strptr("Answer concisely.\nPreserve whitespace.  ")}},
		{"set_host_system_prompt_empty.json", TypeSetHostSystemPrompt, 0, &SetHostSystemPromptPayload{SystemPrompt: strptr("")}},
		{"host_system_prompt.json", TypeHostSystemPrompt, 101, &HostSystemPromptPayload{SystemPrompt: "Answer concisely.\nPreserve whitespace.  ", DefaultSystemPrompt: "Keep the main thread free."}},
		{"host_system_prompt_empty.json", TypeHostSystemPrompt, 102, &HostSystemPromptPayload{SystemPrompt: "", DefaultSystemPrompt: "Keep the main thread free."}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			raw := readFixture(t, tc.fixture)
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.Type != tc.typ {
				t.Fatalf("type = %q, want %q", env.Type, tc.typ)
			}
			if tc.replyID == 0 {
				if env.InReplyTo != nil {
					t.Fatalf("request in_reply_to = %d, want absent", *env.InReplyTo)
				}
			} else if env.InReplyTo == nil || *env.InReplyTo != tc.replyID {
				t.Fatalf("in_reply_to = %v, want %d", env.InReplyTo, tc.replyID)
			}
			got := reflect.New(reflect.TypeOf(tc.want).Elem()).Interface()
			if err := json.Unmarshal(env.Payload, got); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("payload = %#v, want %#v", got, tc.want)
			}
			roundTripEnvelope(t, env, got, raw)
		})
	}
}

// Constructed values pin the entire key set independently of fixture updates.
func TestHostSystemPromptPayloads_WireKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		payload any
		want    map[string]any
	}{
		{"read", RequestHostSystemPromptPayload{}, map[string]any{}},
		{"write", SetHostSystemPromptPayload{SystemPrompt: strptr("custom")}, map[string]any{"system_prompt": "custom"}},
		{"clear", SetHostSystemPromptPayload{SystemPrompt: strptr("")}, map[string]any{"system_prompt": ""}},
		{"missing write value", SetHostSystemPromptPayload{}, map[string]any{"system_prompt": nil}},
		{"reply", HostSystemPromptPayload{SystemPrompt: "custom", DefaultSystemPrompt: "default"}, map[string]any{"system_prompt": "custom", "default_system_prompt": "default"}},
		{"empty current", HostSystemPromptPayload{DefaultSystemPrompt: "default"}, map[string]any{"system_prompt": "", "default_system_prompt": "default"}},
		{"zero reply", HostSystemPromptPayload{}, map[string]any{"system_prompt": "", "default_system_prompt": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode keys: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("wire fields = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSetHostSystemPromptPayload_Decode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		want    *string
		wantErr bool
	}{
		{"missing", `{}`, nil, false},
		{"null", `{"system_prompt":null}`, nil, false},
		{"empty", `{"system_prompt":""}`, strptr(""), false},
		{"populated", `{"system_prompt":"custom"}`, strptr("custom"), false},
		{"whitespace and unicode", `{"system_prompt":" \t规则\n\n"}`, strptr(" \t规则\n\n"), false},
		{"boolean", `{"system_prompt":false}`, nil, true},
		{"number", `{"system_prompt":123}`, nil, true},
		{"object", `{"system_prompt":{}}`, nil, true},
		{"array", `{"system_prompt":[]}`, nil, true},
		{"malformed", `{"system_prompt":`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got SetHostSystemPromptPayload
			err := json.Unmarshal([]byte(tc.raw), &got)
			if (err != nil) != tc.wantErr {
				t.Fatalf("decode error = %v, want error %t", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, SetHostSystemPromptPayload{SystemPrompt: tc.want}) {
				t.Errorf("payload = %#v, want SystemPrompt %#v", got, tc.want)
			}
		})
	}
}

func TestHostSystemPromptPayload_EnvelopeFit(t *testing.T) {
	t.Parallel()
	// NUL expands to six JSON bytes per input byte, the maximum expansion.
	current := strings.Repeat("\x00", conversations.MaxSystemPromptBytes)
	encodedCurrent, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("marshal current: %v", err)
	}
	if len(encodedCurrent) != 6*conversations.MaxSystemPromptBytes+2 {
		t.Fatalf("current does not exercise worst-case escaping: %d bytes", len(encodedCurrent))
	}
	// The default getter is independent of pool initialization and storage.
	defaultPrompt := new(sessions.Pool).DefaultDaemonInstructions()
	if defaultPrompt == "" {
		t.Fatal("shipped default must be nonempty")
	}
	want := HostSystemPromptPayload{SystemPrompt: current, DefaultSystemPrompt: defaultPrompt}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	replyID := uint64(math.MaxUint64)
	raw, err := json.Marshal(Envelope{
		ID: math.MaxUint64, Type: TypeHostSystemPrompt,
		TS:      time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		Payload: payload, InReplyTo: &replyID,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	t.Logf("host prompt envelope: %d bytes, cap %d, current %d bytes, default %d bytes", len(raw), maxV2AppEnvelope, len(current), len(defaultPrompt))
	if len(raw) > maxV2AppEnvelope {
		t.Fatalf("envelope = %d bytes, exceeds cap %d", len(raw), maxV2AppEnvelope)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var got HostSystemPromptPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("envelope round trip changed current or shipped default")
	}
}
