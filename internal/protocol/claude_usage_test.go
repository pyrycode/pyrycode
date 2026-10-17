package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestClaudeUsagePayload_Wire(t *testing.T) {
	t.Parallel()
	zero, frac, over := 0.0, 0.44, 1.7
	resets, past := int64(1790367600), int64(1000)
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		p    ClaudeUsagePayload
		want string
	}{
		{"none carries only state", ClaudeUsagePayload{State: ClaudeUsageStateNone}, `{"state":"none"}`},
		{
			"both windows",
			ClaudeUsagePayload{State: ClaudeUsageStateObserved, ObservedAt: &at,
				FiveHour: &ClaudeUsageWindow{Utilization: &frac, ResetsAt: &resets},
				SevenDay: &ClaudeUsageWindow{Utilization: &over, ResetsAt: &past}},
			`{"state":"observed","observed_at":"2026-10-17T12:00:00Z","five_hour":{"utilization":0.44,"resets_at":1790367600},"seven_day":{"utilization":1.7,"resets_at":1000}}`,
		},
		{
			"absent window and value omitted, zero kept",
			ClaudeUsagePayload{State: ClaudeUsageStateObserved, ObservedAt: &at,
				SevenDay: &ClaudeUsageWindow{Utilization: &zero}},
			`{"state":"observed","observed_at":"2026-10-17T12:00:00Z","seven_day":{"utilization":0}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.p)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("wire = %s\nwant   %s", got, tc.want)
			}
		})
	}
	req, err := json.Marshal(RequestClaudeUsagePayload{})
	if err != nil || string(req) != `{}` {
		t.Fatalf("request wire = %s, %v; want {}", req, err)
	}
}
