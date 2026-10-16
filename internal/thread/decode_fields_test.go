package thread

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testRecordedChild struct {
	Text string `json:"text"`
}

type testRecordedDTO struct {
	Text     string                       `json:"text"`
	Child    testRecordedChild            `json:"child"`
	Optional *testRecordedChild           `json:"optional"`
	Items    []testRecordedChild          `json:"items"`
	Lookup   map[string]testRecordedChild `json:"lookup"`
	When     time.Time                    `json:"when"`
}

// testDecodeReparsed preserves the root-reparse path as an acceptance/cost control.
func testDecodeReparsed(raw json.RawMessage, dst any, required ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, dst) != nil {
		return false
	}
	for _, key := range required {
		if v, ok := fields[key]; !ok || string(v) == "null" {
			return false
		}
	}
	return (foldDecoder{}).validRecordedFields(raw, reflect.TypeOf(dst).Elem())
}

func TestDecodeRecordedFields(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"text":"ok"}`, true},
		{`{"text":"ok","CHILD":{"TEXT":"nested"}}`, true},
		{`{"text":"ok","optional":null,"items":null,"lookup":null}`, true},
		{`{"text":"ok","optional":{"text":"x"},"items":[{"text":"y"}],"lookup":{"k":{"text":"z"}}}`, true},
		{`{"text":"ok","unknown":{"text":null,"items":[null]}}`, true},
		{`{"text":"ok","when":"2026-10-16T00:00:00Z"}`, true},
		{`{"text":null,"text":"ok"}`, true},
		{`{}`, false}, {`{"TEXT":"ok"}`, false},
		{`{"text":null}`, false}, {`{"text":"ok","TEXT":null}`, false},
		{`{"text":"ok","child":null}`, false},
		{`{"text":"ok","child":{"text":null}}`, false},
		{`{"text":"ok","optional":{"text":null}}`, false},
		{`{"text":"ok","items":[null]}`, false},
		{`{"text":"ok","items":[{"text":null}]}`, false},
		{`{"text":"ok","lookup":{"k":null}}`, false},
		{`{"text":"ok","lookup":{"k":{"text":null}}}`, false},
		{`{"text":"ok","when":null}`, false},
		{`{"text":"ok","when":"invalid"}`, false},
		{`{"text":3}`, false}, {`{"text":"ok","items":{}}`, false},
		{`{"text":"ok","unknown":NaN}`, false},
		{`{"text":"ok"} {}`, false}, {`{"text":"ok",}`, false},
		{`null`, false}, {`[]`, false}, {`true`, false}, {``, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var got, before testRecordedDTO
			old := testDecodeReparsed([]byte(tc.raw), &before, "text")
			accepted := decode([]byte(tc.raw), &got, "text")
			if old != tc.want || accepted != old || (accepted && !reflect.DeepEqual(got, before)) {
				t.Fatalf("acceptance: got %v, control %v, want %v; DTOs: %#v / %#v", accepted, old, tc.want, got, before)
			}
		})
	}
}

func TestDecodeRecordedAllocations(t *testing.T) {
	raw := []byte(`{"text":"` + strings.Repeat("x", 65536) + `","unknown":{"nested":[1,2,null]}}`)
	measure := func(fn func(json.RawMessage, any, ...string) bool) float64 {
		return testing.AllocsPerRun(100, func() {
			var dst testRecordedDTO
			if !fn(raw, &dst, "text") {
				t.Fatal("valid object rejected")
			}
		})
	}
	before, after := measure(testDecodeReparsed), measure(decode)
	t.Logf("root-reparse allocations=%g reused-root allocations=%g", before, after)
	if after >= before*0.9 {
		t.Fatalf("root field reuse did not remove duplicate allocations: %g versus %g", after, before)
	}
}

func BenchmarkDecodeRecordedFields(b *testing.B) {
	raw := []byte(`{"text":"` + strings.Repeat("x", 65536) + `","child":{"text":"nested"},"unknown":[null,{}]}`)
	for _, tc := range []struct {
		name string
		fn   func(json.RawMessage, any, ...string) bool
	}{{"reparsed", testDecodeReparsed}, {"reused", decode}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var dst testRecordedDTO
				if !tc.fn(raw, &dst, "text") {
					b.Fatal("valid object rejected")
				}
			}
		})
	}
}
