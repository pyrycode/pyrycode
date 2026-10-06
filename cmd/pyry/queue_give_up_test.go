package main

import (
	"testing"
	"time"
)

func TestParseQueueGiveUpAfter(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{raw: "", want: 0},
		{raw: "3s", want: 3 * time.Second},
		{raw: "1500ms", want: 1500 * time.Millisecond},
		{raw: "0s", wantErr: true},
		{raw: "-1s", wantErr: true},
		{raw: "3", wantErr: true},
		{raw: "soon", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseQueueGiveUpAfter(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseQueueGiveUpAfter(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseQueueGiveUpAfter(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

// An ordinary build must not honour the override, whatever the environment says.
func TestQueueGiveUpAfterIgnoredInOrdinaryBuild(t *testing.T) {
	t.Setenv(queueGiveUpAfterEnv, "1s")
	got, err := queueGiveUpAfter()
	if err != nil || got != 0 {
		t.Fatalf("queueGiveUpAfter() = %s, %v; want 0, nil in an untagged build", got, err)
	}
}
