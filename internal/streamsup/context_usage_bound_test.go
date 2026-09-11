package streamsup

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type contextUsageBoundFixture struct {
	id     string
	name   string
	server string
	tokens int
}

func contextUsageFixtureWeight(entry contextUsageBoundFixture) int {
	return entry.tokens
}

func contextUsageFixtureStrings(entry *contextUsageBoundFixture) []*string {
	return []*string{&entry.name, &entry.server}
}

func TestBoundContextUsageEntries_RanksBeforeCountCut(t *testing.T) {
	t.Parallel()

	entries := make([]contextUsageBoundFixture, 0, 36)
	for i := range 34 {
		entries = append(entries, contextUsageBoundFixture{
			id:     fmt.Sprintf("entry-%02d", i),
			name:   fmt.Sprintf("name-%02d", i),
			server: "fixture",
			tokens: i,
		})
	}
	entries = append(entries,
		contextUsageBoundFixture{id: "equal-first", name: "first", server: "fixture", tokens: 100},
		contextUsageBoundFixture{id: "equal-second", name: "second", server: "fixture", tokens: 100},
	)
	original := append([]contextUsageBoundFixture(nil), entries...)

	got, dropped := boundContextUsageEntries(entries, contextUsageFixtureWeight, contextUsageFixtureStrings)

	if dropped != 4 {
		t.Fatalf("dropped = %d, want 4", dropped)
	}
	if len(got) != 32 {
		t.Fatalf("len(result) = %d, want 32", len(got))
	}
	if cap(got) != len(got) {
		t.Errorf("cap(result) = %d, want retained length %d", cap(got), len(got))
	}
	wantIDs := []string{"equal-first", "equal-second"}
	for i := 33; i >= 4; i-- {
		wantIDs = append(wantIDs, fmt.Sprintf("entry-%02d", i))
	}
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.id)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("retained ids = %v, want %v", gotIDs, wantIDs)
	}
	if !reflect.DeepEqual(entries, original) {
		t.Errorf("input mutated:\n got  %+v\n want %+v", entries, original)
	}
}

func TestBoundContextUsageEntries_StringByteBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		entry       contextUsageBoundFixture
		wantRetain  bool
		wantDropped int
	}{
		{
			name: "both designated fields at 256 bytes are retained",
			entry: contextUsageBoundFixture{
				name: strings.Repeat("n", 256), server: strings.Repeat("s", 256), tokens: 1,
			},
			wantRetain: true,
		},
		{
			name: "first designated field at 257 bytes rejects the entry",
			entry: contextUsageBoundFixture{
				name: strings.Repeat("n", 257), server: strings.Repeat("s", 256), tokens: 2,
			},
			wantDropped: 1,
		},
		{
			name: "second designated field at 257 bytes rejects the entry",
			entry: contextUsageBoundFixture{
				name: strings.Repeat("n", 256), server: strings.Repeat("s", 257), tokens: 3,
			},
			wantDropped: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, dropped := boundContextUsageEntries(
				[]contextUsageBoundFixture{tc.entry},
				contextUsageFixtureWeight,
				contextUsageFixtureStrings,
			)
			if dropped != tc.wantDropped {
				t.Errorf("dropped = %d, want %d", dropped, tc.wantDropped)
			}
			if retained := len(got) == 1; retained != tc.wantRetain {
				t.Errorf("retained = %v, want %v; result = %+v", retained, tc.wantRetain, got)
			}
		})
	}
}

func TestBoundContextUsageEntries_CombinesRejectionAndCountDrops(t *testing.T) {
	t.Parallel()

	entries := make([]contextUsageBoundFixture, 0, 35)
	entries = append(entries, contextUsageBoundFixture{
		id: "overlong-heavy", name: strings.Repeat("x", 257), server: "fixture", tokens: 10_000,
	})
	for i := range 34 {
		entries = append(entries, contextUsageBoundFixture{
			id: fmt.Sprintf("valid-%02d", i), name: "valid", server: "fixture", tokens: i,
		})
	}

	got, dropped := boundContextUsageEntries(entries, contextUsageFixtureWeight, contextUsageFixtureStrings)

	if dropped != len(entries)-len(got) || dropped != 3 {
		t.Fatalf("dropped = %d, want len(input)-len(result) = %d and exact count 3", dropped, len(entries)-len(got))
	}
	if got[0].id != "valid-33" || got[len(got)-1].id != "valid-02" {
		t.Errorf("survivor range = %q through %q, want valid-33 through valid-02", got[0].id, got[len(got)-1].id)
	}
	for _, entry := range got {
		if entry.id == "overlong-heavy" {
			t.Fatal("overlong entry survived because its token weight was high")
		}
	}
}

func TestBoundContextUsageEntries_ResultOwnsRetainedValues(t *testing.T) {
	t.Parallel()

	entries := []contextUsageBoundFixture{
		{id: "kept", name: strings.Repeat("n", 256), server: "server", tokens: 2},
		{id: "also-kept", name: "other", server: "server", tokens: 1},
	}
	got, dropped := boundContextUsageEntries(entries, contextUsageFixtureWeight, contextUsageFixtureStrings)
	if dropped != 0 || len(got) != 2 {
		t.Fatalf("result = %+v, dropped = %d; want two retained and zero dropped", got, dropped)
	}
	want := append([]contextUsageBoundFixture(nil), got...)

	entries[0].name = "mutated-name"
	entries[0].server = "mutated-server"
	entries[1] = contextUsageBoundFixture{id: "replacement", tokens: 99}
	entries = append(entries, contextUsageBoundFixture{id: "new-entry"})

	if !reflect.DeepEqual(got, want) {
		t.Errorf("result changed after input mutation:\n got  %+v\n want %+v", got, want)
	}
}

func TestBoundContextUsageEntries_EmptyInput(t *testing.T) {
	t.Parallel()

	for _, entries := range [][]contextUsageBoundFixture{nil, {}} {
		got, dropped := boundContextUsageEntries(entries, contextUsageFixtureWeight, contextUsageFixtureStrings)
		if got != nil || dropped != 0 {
			t.Errorf("boundContextUsageEntries(%#v) = (%#v, %d), want (nil, 0)", entries, got, dropped)
		}
	}
}
