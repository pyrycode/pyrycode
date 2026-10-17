package streamsup

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// usageReading is one SetUsageWindowsHandler call as the tests record it.
type usageReading struct {
	fiveHour, sevenDay *UsageWindow
}

// usageParser returns a parser recording both its events and its usage readings.
func usageParser(log *slog.Logger) (*Parser, *[]turnevent.Event, *[]usageReading) {
	var events []turnevent.Event
	var readings []usageReading
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, log)
	p.SetUsageWindowsHandler(func(fiveHour, sevenDay *UsageWindow) {
		readings = append(readings, usageReading{fiveHour, sevenDay})
	})
	return p, &events, &readings
}

// capturedAllowedRateLimitLine returns the committed benign rate_limit_event line
// from claude 2.1.280, byte for byte as claude wrote it.
func capturedAllowedRateLimitLine(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "e2e", "realclaude", "testdata", "subagent_prompt_v2.1.280.json"))
	if err != nil {
		t.Fatalf("reading capture: %v", err)
	}
	var rec struct {
		Frames []struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decoding capture: %v", err)
	}
	var lines []string
	for _, f := range rec.Frames {
		if f.Type == "rate_limit_event" {
			lines = append(lines, f.Payload)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("rate_limit_event frames in capture: got %d, want 1", len(lines))
	}
	if !strings.Contains(lines[0], `"status":"allowed"`) || !strings.Contains(lines[0], `"unifiedWindows"`) {
		t.Fatalf("captured line is not the benign windows-bearing reading: %s", lines[0])
	}
	return lines[0]
}

func usageWindowLine(windows string) string {
	return `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1790367600,"rateLimitType":"five_hour","unifiedWindows":` + windows + `}}`
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func TestParser_UsageWindowsFromCapturedAllowedLine(t *testing.T) {
	t.Parallel()
	p, events, readings := usageParser(nil)
	if _, err := p.Write([]byte(capturedAllowedRateLimitLine(t) + "\n")); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatalf("events = %#v, want none: the benign gate must still hold", *events)
	}
	want := []usageReading{{
		fiveHour: &UsageWindow{Utilization: f64(0.44), ResetsAt: i64(1790367600)},
		sevenDay: &UsageWindow{Utilization: f64(0.64), ResetsAt: i64(1790575200)},
	}}
	if !reflect.DeepEqual(*readings, want) {
		t.Fatalf("readings = %s, want %s", dumpReadings(*readings), dumpReadings(want))
	}
}

func TestParser_UsageWindowsValuesVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		windows string
		want    usageReading
	}{
		{
			name:    "seven_day absent",
			windows: `{"five_hour":{"utilization":0.13,"resetsAt":1790367600}}`,
			want:    usageReading{fiveHour: &UsageWindow{Utilization: f64(0.13), ResetsAt: i64(1790367600)}},
		},
		{
			name:    "five_hour absent, zero utilization kept",
			windows: `{"seven_day":{"utilization":0,"resetsAt":1790575200}}`,
			want:    usageReading{sevenDay: &UsageWindow{Utilization: f64(0), ResetsAt: i64(1790575200)}},
		},
		{
			name:    "past resetsAt and utilization above 1 pass unchanged",
			windows: `{"five_hour":{"utilization":1.7,"resetsAt":1000},"seven_day":{"resetsAt":-5}}`,
			want: usageReading{
				fiveHour: &UsageWindow{Utilization: f64(1.7), ResetsAt: i64(1000)},
				sevenDay: &UsageWindow{ResetsAt: i64(-5)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, _, readings := usageParser(nil)
			if _, err := p.Write([]byte(usageWindowLine(tt.windows) + "\n")); err != nil {
				t.Fatal(err)
			}
			want := []usageReading{tt.want}
			if !reflect.DeepEqual(*readings, want) {
				t.Fatalf("readings = %s, want %s", dumpReadings(*readings), dumpReadings(want))
			}
		})
	}
}

func TestParser_UsageWindowsKeepsPreviousReading(t *testing.T) {
	t.Parallel()
	const needle = "8675309"
	tests := []struct {
		name string
		line string
		log  bool
	}{
		{name: "no unifiedWindows", line: rateLimitLineFixture(t, "allowed", "five_hour", 1790367600)},
		{name: "null unifiedWindows", line: usageWindowLine(`null`)},
		{name: "neither window", line: usageWindowLine(`{"overage":{"utilization":0.5}}`)},
		{name: "undecodable unifiedWindows", line: usageWindowLine(`{"five_hour":{"utilization":"0.` + needle + `"}}`), log: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			p, _, readings := usageParser(slog.New(rec))
			first := usageWindowLine(`{"five_hour":{"utilization":0.2,"resetsAt":1}}`)
			if _, err := p.Write([]byte(first + "\n" + tt.line + "\n")); err != nil {
				t.Fatal(err)
			}
			if len(*readings) != 1 {
				t.Fatalf("handler calls = %d (%s), want 1: the second line must leave the reading alone",
					len(*readings), dumpReadings(*readings))
			}
			drops := rec.withMessage(usageWindowsDropMsg)
			if tt.log != (len(drops) == 1) || len(drops) > 1 {
				t.Fatalf("usage drop records = %d, want log=%v", len(drops), tt.log)
			}
			for _, r := range rec.all() {
				for k, v := range r.attrs {
					if strings.Contains(v, needle) {
						t.Errorf("record %q attr %q leaks the reading", r.msg, k)
					}
				}
			}
		})
	}
}

// A malformed windows object must not change what the line does to rate_limited:
// the warning still emits exactly as it does with no windows at all.
func TestParser_UsageWindowsMalformedLeavesRateLimitedAlone(t *testing.T) {
	t.Parallel()
	p, events, readings := usageParser(nil)
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1790575200,"rateLimitType":"seven_day","utilization":0.76,"unifiedWindows":{"five_hour":[1]}}}`
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	if len(*readings) != 0 {
		t.Fatalf("readings = %s, want none", dumpReadings(*readings))
	}
	want := []turnevent.Event{turnevent.RateLimited{Status: "allowed_warning", LimitType: "seven_day", ResetsAt: 1790575200, Utilization: f64(0.76)}}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events = %#v, want %#v", *events, want)
	}
}

func dumpReadings(rs []usageReading) string {
	type w struct {
		FiveHour, SevenDay *UsageWindow
	}
	out := make([]w, len(rs))
	for i, r := range rs {
		out[i] = w{r.fiveHour, r.sevenDay}
	}
	b, _ := json.Marshal(out)
	return string(b)
}
