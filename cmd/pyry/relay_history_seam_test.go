package main

import (
	"fmt"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestHistoryPageFailure_NamesEverySentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrapped: %w", history.ErrInvalidCursor), "invalid_cursor"},
		{fmt.Errorf("wrapped: %w", history.ErrInvalidID), "invalid_id"},
		{fmt.Errorf("wrapped: %w", history.ErrInvalidPageSize), "invalid_page_size"},
		{fmt.Errorf("wrapped: %w", history.ErrNotContained), "not_contained"},
		{fmt.Errorf("wrapped: %w", history.ErrCorruptSegment), "corrupt_segment"},
		{fmt.Errorf("wrapped: %w", history.ErrUnknownVersion), "unknown_version"},
		{fmt.Errorf("open /var/log/seg-0001.jsonl: permission denied"), "read"},
	}
	for _, tt := range tests {
		if got := historyPageFailure(tt.err); got != tt.want {
			t.Errorf("historyPageFailure(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
