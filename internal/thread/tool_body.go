package thread

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

var (
	ErrInvalidBodyScope = errors.New("thread: body requires epoch, item ID and revision")
	ErrStaleBody        = errors.New("thread: body epoch or revision is stale")
	ErrBodyNotFound     = errors.New("thread: body item is not public")
	ErrBodyKind         = errors.New("thread: body item must be tool_call or agent")
)

// ToolBodyResult carries one detached complete body from one publication.
// Only StateUsable carries ID, Rev, Content, Epoch and consumed Version.
// Refusals and cancellation return a zero result and a separate error.
type ToolBodyResult struct {
	State   SnapshotState
	ID, Rev uint64
	Content json.RawMessage
	Epoch   string
	Version uint64
	Err     error
}

// ToolBody reads a public tool_call or agent at the expected epoch/revision.
// Hidden and dropped rows remain public. Only the selected body and its recipe
// dependencies are rendered; no parent or descendant rows are returned.
// The caller must already be authorized for id. This read never loads or waits
// for recovery, and unrelated consumed-version advances do not invalidate it.
func (s *Store) ToolBody(ctx context.Context, id conversations.ConversationID, expectedEpoch string, itemID, expectedRev uint64) (ToolBodyResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolBodyResult{}, err
	}
	if !conversations.ValidID(string(id)) {
		return ToolBodyResult{}, history.ErrInvalidID
	}
	if expectedEpoch == "" || itemID == 0 || expectedRev == 0 {
		return ToolBodyResult{}, ErrInvalidBodyScope
	}
	snap, q := s.captureQuery(id)
	if snap.State != StateUsable {
		return ToolBodyResult{State: snap.State, Err: snap.Err}, nil
	}
	if snap.Epoch != expectedEpoch {
		return ToolBodyResult{}, ErrStaleBody
	}
	row, ok := q.lookup(itemID, false)
	if !ok {
		return ToolBodyResult{}, ErrBodyNotFound
	}
	if row.item.Rev != expectedRev {
		return ToolBodyResult{}, ErrStaleBody
	}
	if row.item.Kind != "tool_call" && row.item.Kind != "agent" {
		return ToolBodyResult{}, ErrBodyKind
	}
	result, err := s.renderQuery(ctx, id, q, QueryResult{State: StateUsable, Items: []Item{*row.item}, Epoch: snap.Epoch, Version: snap.Version}, nil)
	if err != nil {
		return ToolBodyResult{}, err
	}
	if result.State != StateUsable {
		return ToolBodyResult{State: result.State, Err: result.Err}, nil
	}
	item := result.Items[0]
	return ToolBodyResult{State: StateUsable, ID: item.ID, Rev: item.Rev, Content: item.Content, Epoch: result.Epoch, Version: result.Version}, nil
}
