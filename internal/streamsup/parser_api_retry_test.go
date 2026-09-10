package streamsup

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	apiRetryAttemptOne = `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10}`
	apiRetryAttemptTwo = `{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10}`
	apiRetryAssistant  = `{"type":"assistant","message":{"id":"msg-retry","role":"assistant","content":[{"type":"text","text":"recovered"}]}}`
	apiRetryUser       = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-retry","content":"done","is_error":false}]}}`
	apiRetryResult     = `{"type":"result","subtype":"success"}`
)

func apiRetryTrace(ev turnevent.Event) string {
	switch e := ev.(type) {
	case turnevent.ApiRetry:
		return fmt.Sprintf("retry:%t:%d/%d", e.Active, e.Current, e.Total)
	case turnevent.TextChunk:
		return "assistant"
	case turnevent.ToolUpdate:
		return "user"
	case turnevent.TurnEnd:
		return "result"
	case turnevent.Unrecognized:
		return "unrecognized"
	default:
		return fmt.Sprintf("%T", ev)
	}
}

func apiRetryRun(t *testing.T, lines ...string) []string {
	t.Helper()
	var got []string
	p := NewParser(func(ev turnevent.Event) {
		got = append(got, apiRetryTrace(ev))
	}, discardLogger())
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write(%.40s) err = %v, want nil", line, err)
		}
	}
	return got
}

func TestParser_APIRetryEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{
			name:  "every retry line advances the active counter",
			lines: []string{apiRetryAttemptOne, apiRetryAttemptTwo, apiRetryResult},
			want:  []string{"retry:true:1/10", "retry:true:2/10", "retry:false:0/0", "result"},
		},
		{
			name:  "assistant clears before its event",
			lines: []string{apiRetryAttemptOne, apiRetryAssistant},
			want:  []string{"retry:true:1/10", "retry:false:0/0", "assistant"},
		},
		{
			name:  "user clears before its event",
			lines: []string{apiRetryAttemptOne, apiRetryUser},
			want:  []string{"retry:true:1/10", "retry:false:0/0", "user"},
		},
		{
			name:  "result clears before its event",
			lines: []string{apiRetryAttemptOne, apiRetryResult},
			want:  []string{"retry:true:1/10", "retry:false:0/0", "result"},
		},
		{
			name:  "later content emits no duplicate clear",
			lines: []string{apiRetryAttemptOne, apiRetryAssistant, apiRetryUser, apiRetryResult},
			want:  []string{"retry:true:1/10", "retry:false:0/0", "assistant", "user", "result"},
		},
		{
			name:  "content without an open retry emits no clear",
			lines: []string{apiRetryAssistant, apiRetryUser, apiRetryResult},
			want:  []string{"assistant", "user", "result"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := apiRetryRun(t, tc.lines...)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParser_APIRetryCounterFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{"valid counters", apiRetryAttemptOne, "retry:true:1/10"},
		{"absent counters", `{"type":"system","subtype":"api_retry"}`, "retry:true:0/0"},
		{"non-integer attempt", `{"type":"system","subtype":"api_retry","attempt":"one","max_retries":10}`, "retry:true:0/0"},
		{"non-integer total", `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10.5}`, "retry:true:0/0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := apiRetryRun(t, tc.line)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("events = %v, want [%s] (the known subtype must be consumed)", got, tc.want)
			}
		})
	}
}
