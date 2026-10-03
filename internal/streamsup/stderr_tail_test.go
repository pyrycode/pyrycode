package streamsup

import (
	"fmt"
	"strings"
	"testing"
)

func TestStderrTail(t *testing.T) {
	t.Parallel()
	numbered := func(n, width int) []string {
		var out []string
		for i := range n {
			line := fmt.Sprintf("line-%03d-", i)
			out = append(out, line+strings.Repeat("x", width-len(line)))
		}
		return out
	}
	long := strings.Join(numbered(10, 300), "\n") + "\n"
	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{name: "empty", writes: nil, want: ""},
		{name: "one line, trailing newline trimmed", writes: []string{"error: unknown option '--bogus'\n"}, want: "error: unknown option '--bogus'"},
		{name: "CRLF trimmed", writes: []string{"boom\r\n"}, want: "boom"},
		{
			name:   "more than five lines keeps the last five",
			writes: []string{"a\nb\nc\n", "d\ne\nf\ng\n"},
			want:   "c\nd\ne\nf\ng",
		},
		{
			name:   "a line split across writes is rejoined",
			writes: []string{"first\nsec", "ond\n"},
			want:   "first\nsecond",
		},
		{
			name:   "more than the byte cap keeps the end",
			writes: []string{long},
			want:   strings.TrimRight(long[len(long)-stderrTailBytes:], "\n"),
		},
		{
			name:   "byte cap holds across many small writes",
			writes: numbered(10, 300),
			want:   strings.Join(numbered(10, 300), "")[10*300-stderrTailBytes:],
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tail stderrTail
			for _, w := range tc.writes {
				if n, err := tail.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write(%d bytes) = (%d, %v), want (%d, nil)", len(w), n, err, len(w))
				}
			}
			got := tail.String()
			if got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
			if len(got) > stderrTailBytes {
				t.Errorf("len(String()) = %d, want <= %d", len(got), stderrTailBytes)
			}
			if n := strings.Count(got, "\n") + 1; got != "" && n > stderrTailLines {
				t.Errorf("String() has %d lines, want <= %d", n, stderrTailLines)
			}
		})
	}
}
