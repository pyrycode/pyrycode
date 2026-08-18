package conversations

import (
	"testing"
	"time"
)

func TestShouldArchive(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		c    Conversation
		want bool
	}{
		{
			name: "promoted, very idle",
			c: Conversation{
				IsPromoted: true,
				LastUsedAt: now.Add(-365 * 24 * time.Hour),
			},
			want: false,
		},
		{
			name: "unpromoted, exactly 30 days idle",
			c: Conversation{
				IsPromoted: false,
				LastUsedAt: now.Add(-30 * 24 * time.Hour),
			},
			want: true,
		},
		{
			name: "unpromoted, 29d23h idle",
			c: Conversation{
				IsPromoted: false,
				LastUsedAt: now.Add(-(29*24*time.Hour + 23*time.Hour)),
			},
			want: false,
		},
		{
			name: "unpromoted, just over threshold",
			c: Conversation{
				IsPromoted: false,
				LastUsedAt: now.Add(-(30*24*time.Hour + time.Second)),
			},
			want: true,
		},
		{
			// IsPromoted must stay false: a promoted row would be absorbed by
			// the IsPromoted short-circuit and prove nothing about the
			// manual-archive exemption.
			name: "archived, very idle",
			c: Conversation{
				IsPromoted: false,
				IsArchived: true,
				LastUsedAt: now.Add(-365 * 24 * time.Hour),
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldArchive(tc.c, now)
			if got != tc.want {
				t.Errorf("ShouldArchive = %v, want %v", got, tc.want)
			}
		})
	}
}
