package thread

import (
	"encoding/json"
	"testing"
)

func TestDecodeEmptyObject(t *testing.T) {
	if decode([]byte(`{}`), (*struct{})(nil)) {
		t.Fatal("nil destination accepted")
	}
	for _, raw := range []string{
		`{}`, " \t\r\n{} \n", `{"unknown":null}`, `{"text":"a","text":null}`,
		`{"nested":[null,{},[true,1,"x"]]}`, `{"escape":"\u0000\uD800"}`,
		``, `null`, `[]`, `"object"`, `true`, `123`, `{`, `{"x":}`,
		`{} {}`, `{} garbage`, "\u00a0{}", "\v{}", `{"x":NaN}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var fields map[string]json.RawMessage
			var empty struct{}
			want := json.Unmarshal([]byte(raw), &fields) == nil && fields != nil && json.Unmarshal([]byte(raw), &empty) == nil
			if got := decode([]byte(raw), &struct{}{}); got != want {
				t.Fatalf("object acceptance = %v, want %v", got, want)
			}
		})
	}
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{}`, false}, {`{"required":null}`, false}, {`{"required":false}`, true},
	} {
		if got := decode([]byte(tc.raw), &struct{}{}, "required"); got != tc.want {
			t.Fatalf("required-field acceptance for %s = %v", tc.raw, got)
		}
	}
}

func TestDecodeEmptyObjectAllocations(t *testing.T) {
	raw := []byte(`{"text":"retained source","unknown":{"nested":[1,2,null]}}`)
	var dst struct{}
	if allocations := testing.AllocsPerRun(100, func() {
		if !decode(raw, &dst) {
			t.Fatal("valid object rejected")
		}
	}); allocations > 4 {
		// json.Valid uses a pooled scanner; race builds may discard pooled values.
		// Allow scanner/depth storage, but no per-field object maps or DTO decode.
		t.Fatalf("empty DTO object validation allocates %g times", allocations)
	}
}

func BenchmarkDecodeEmptyObject(b *testing.B) {
	raw := []byte(`{"text":"retained source","unknown":{"nested":[1,2,null]}}`)
	var dst struct{}
	b.ReportAllocs()
	for b.Loop() {
		if !decode(raw, &dst) {
			b.Fatal("valid object rejected")
		}
	}
}
