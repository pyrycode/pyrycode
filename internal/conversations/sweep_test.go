package conversations

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestSweep(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	type seedSpec struct {
		idleDays   int
		isPromoted bool
		isArchived bool
	}

	// survivorSpec identifies a row expected to outlive the sweep by the
	// deterministic Cwd mk assigns, paired with its archive flag.
	type survivorSpec struct {
		cwd        string
		isArchived bool
	}

	mk := func(specs []seedSpec) *Registry {
		r := &Registry{}
		for i, s := range specs {
			r.Create(Conversation{
				ID:         ConversationID(fmt.Sprintf("%08d-2222-4333-8444-555555555555", i)),
				Cwd:        fmt.Sprintf("/seed-%d", i),
				IsPromoted: s.isPromoted,
				IsArchived: s.isArchived,
				LastUsedAt: now.Add(-time.Duration(s.idleDays) * 24 * time.Hour),
			})
		}
		return r
	}

	tests := []struct {
		name      string
		seeds     []seedSpec
		wantCount int
		// wantSurvivors, when non-nil, pins which rows survive, not just how
		// many. Rows whose scenario is fully described by wantCount leave it nil.
		wantSurvivors []survivorSpec
	}{
		{
			name:      "empty-registry",
			seeds:     nil,
			wantCount: 0,
		},
		{
			name: "all-archivable",
			seeds: []seedSpec{
				{idleDays: 31, isPromoted: false},
				{idleDays: 31, isPromoted: false},
				{idleDays: 31, isPromoted: false},
			},
			wantCount: 3,
		},
		{
			name: "none-archivable-fresh",
			seeds: []seedSpec{
				{idleDays: 7, isPromoted: false},
				{idleDays: 7, isPromoted: false},
			},
			wantCount: 0,
		},
		{
			name: "none-archivable-promoted-but-idle",
			seeds: []seedSpec{
				{idleDays: 365, isPromoted: true},
				{idleDays: 365, isPromoted: true},
			},
			wantCount: 0,
		},
		{
			name: "mixed",
			seeds: []seedSpec{
				{idleDays: 31, isPromoted: false},
				{idleDays: 31, isPromoted: false},
				{idleDays: 7, isPromoted: false},
				{idleDays: 7, isPromoted: false},
				{idleDays: 365, isPromoted: true},
				{idleDays: 365, isPromoted: true},
			},
			wantCount: 2,
		},
		{
			name: "boundary-exactly-30-days",
			seeds: []seedSpec{
				{idleDays: 30, isPromoted: false},
			},
			wantCount: 1,
		},
		{
			// The count alone does not pin this row: an inverted guard
			// (exempting the non-archived row instead) also removes exactly
			// one. wantSurvivors is what says the right row survived.
			name: "archived-idle-row-survives",
			seeds: []seedSpec{
				{idleDays: 31, isPromoted: false, isArchived: true},
				{idleDays: 31, isPromoted: false, isArchived: false},
			},
			wantCount:     1,
			wantSurvivors: []survivorSpec{{cwd: "/seed-0", isArchived: true}},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := mk(tc.seeds)
			gotCount := Sweep(reg, now)
			if gotCount != tc.wantCount {
				t.Errorf("Sweep returned %d, want %d", gotCount, tc.wantCount)
			}
			survivors := reg.List()
			if got, want := len(survivors), len(tc.seeds)-tc.wantCount; got != want {
				t.Errorf("len(List) after Sweep = %d, want %d", got, want)
			}
			for _, c := range survivors {
				if ShouldArchive(c, now) {
					t.Errorf("survivor %q is archive-eligible: IsPromoted=%v LastUsedAt=%v", c.ID, c.IsPromoted, c.LastUsedAt)
				}
			}
			if tc.wantSurvivors != nil {
				got := make([]survivorSpec, 0, len(survivors))
				for _, c := range survivors {
					got = append(got, survivorSpec{cwd: c.Cwd, isArchived: c.IsArchived})
				}
				if !slices.Equal(got, tc.wantSurvivors) {
					t.Errorf("survivors after Sweep = %+v, want %+v", got, tc.wantSurvivors)
				}
			}
		})
	}
}
