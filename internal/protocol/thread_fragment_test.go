package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestThreadFragmentEscapedBudget(t *testing.T) {
	t.Parallel()
	var ascii strings.Builder
	for c := byte(0); c < utf8.RuneSelf; c++ {
		ascii.WriteByte(c)
	}
	for _, tc := range []struct{ name, text string }{
		{"empty", ""},
		{"ASCII", ascii.String()},
		{"UTF8", "é中🌲\u2028\u2029"},
		{"mixed", "<\\\"\n🌲>&\u2028plain\u2029\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.text)
			encoded, _ := json.Marshal(tc.text)
			for room := -1; room <= len(encoded); room++ {
				want := 0
				for end := 1; end <= len(data); end++ {
					if end < len(data) && !utf8.RuneStart(data[end]) {
						continue
					}
					prefix, _ := json.Marshal(string(data[:end]))
					if len(prefix)-2 <= room {
						want = end
					}
				}
				if got := threadFragmentLength(data, room); got != want {
					t.Fatalf("room=%d: prefix=%d, want %d", room, got, want)
				}
			}
		})
	}
}

func TestThreadFragmentSizingAllocations(t *testing.T) {
	data := []byte(strings.Repeat(`\u003c\\\"\n`, 24000))
	want := threadFragmentLength(data, MaxThreadEnvelopeBytes)
	allocs := testing.AllocsPerRun(10, func() {
		if got := threadFragmentLength(data, MaxThreadEnvelopeBytes); got != want {
			t.Fatalf("unstable fragment boundary: %d != %d", got, want)
		}
	})
	if allocs != 0 {
		t.Fatalf("fragment sizing allocated %.0f times; sizing must scan without serializing candidate prefixes", allocs)
	}
}
