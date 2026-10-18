package main

import (
	"context"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Complete child blocks publish in source order without changing main lifecycle.
// Only the final transport piece completes the coalesced paragraph.
func (e *interactiveTurnEmitterV2) emitCompleteChildText(ctx context.Context, convID string, v turnevent.TextChunk) {
	e.flushDelta(ctx)
	if !e.ensureDeltaLane(convID, v.ParentToolCallID) {
		return
	}
	chunks := splitDeltaText(v.Text, maxDeltaTextBytes)
	for i, text := range chunks {
		v.Text, v.TextComplete = text, i == len(chunks)-1
		turn, seq := e.deltaAddress(v.ParentToolCallID)
		e.emitMappedAt(ctx, convID, v, turn, seq)
		e.advanceDeltaSeq(v.ParentToolCallID)
	}
}
