package thread

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

var (
	ErrInvalidChildScope   = errors.New("thread: child page requires epoch, parent and revision")
	ErrChildParentNotFound = errors.New("thread: public child-page parent not found")
	ErrStaleChildPage      = errors.New("thread: stale child page capture")
	ErrInvalidChildCursor  = errors.New("thread: invalid child page cursor")
)

// ChildSummary is detached metadata. DetailOmitted distinguishes absent detail
// from an available empty body. Headline is plain valid UTF-8, at most 256 bytes.
type ChildSummary struct {
	ID, Order, Rev                uint64
	EndedOrder                    uint64
	Kind, Subtype, Session, Agent string
	NoChild                       bool
	Turn                          string
	Parent                        uint64
	Status                        string
	Active, Shown                 bool
	Headline                      string
	DetailOmitted                 bool
}

// ChildCursor is a position within one exact publication and parent revision.
// It is consistency evidence, not authorization or a wire encoding.
type ChildCursor struct {
	ConversationID             conversations.ConversationID
	Epoch                      string
	Version, Parent, ParentRev uint64
	Order, ID                  uint64
}

// ChildPageResult carries authoritative completion only for StateUsable.
// Rows are strictly direct children, ascending by (Order, ID), without extras.
type ChildPageResult struct {
	State                      SnapshotState
	ConversationID             conversations.ConversationID
	Epoch                      string
	Version, Parent, ParentRev uint64
	Items                      []ChildSummary
	More, Complete             bool
	Err                        error
}

// CursorAfter resumes after the first n returned rows, allowing byte packers to
// deliver a nonempty prefix without skipping selected but undelivered rows.
func (p ChildPageResult) CursorAfter(n int) (ChildCursor, error) {
	if p.State != StateUsable || n <= 0 || n > len(p.Items) {
		return ChildCursor{}, ErrInvalidChildCursor
	}
	row := p.Items[n-1]
	return ChildCursor{ConversationID: p.ConversationID, Epoch: p.Epoch, Version: p.Version, Parent: p.Parent, ParentRev: p.ParentRev, Order: row.Order, ID: row.ID}, nil
}

// ChildPage reads revision-scoped public direct children without rendering or
// history lookups. Callers authorize id; this method never loads or waits.
// Positive limits clamp at 256 and are hard row ceilings, including across ties.
func (s *Store) ChildPage(ctx context.Context, id conversations.ConversationID, epoch string, parent, revision uint64, limit int, continuation *ChildCursor) (ChildPageResult, error) {
	if err := ctx.Err(); err != nil {
		return ChildPageResult{}, err
	}
	if !conversations.ValidID(string(id)) {
		return ChildPageResult{}, history.ErrInvalidID
	}
	if limit <= 0 {
		return ChildPageResult{}, ErrInvalidLimit
	}
	if epoch == "" || parent == 0 || revision == 0 {
		return ChildPageResult{}, ErrInvalidChildScope
	}
	snap, q := s.captureQuery(id)
	if snap.State != StateUsable {
		return ChildPageResult{State: snap.State, Err: snap.Err}, nil
	}
	if q == nil {
		return ChildPageResult{State: StateUnavailable, Err: ErrUnavailable}, nil
	}
	if snap.Epoch != epoch {
		return ChildPageResult{}, ErrStaleChildPage
	}
	row, ok := q.lookup(parent, false)
	if !ok {
		return ChildPageResult{}, ErrChildParentNotFound
	}
	if row.item.Rev != revision {
		return ChildPageResult{}, ErrStaleChildPage
	}
	p := ChildPageResult{State: StateUsable, ConversationID: id, Epoch: epoch, Version: snap.Version, Parent: parent, ParentRev: revision}
	start := q.childRank(parent, 0, 0, false)
	if continuation != nil {
		c := *continuation
		if c.ConversationID != id || c.Epoch != epoch || c.Parent != parent || c.ParentRev != revision || c.ID == 0 {
			return ChildPageResult{}, ErrInvalidChildCursor
		}
		if c.Version != snap.Version {
			return ChildPageResult{}, ErrStaleChildPage
		}
		last, exists := q.lookup(c.ID, false)
		if !exists || last.item.Parent != parent || last.item.Order != c.Order {
			return ChildPageResult{}, ErrInvalidChildCursor
		}
		start = q.childRank(parent, c.Order, c.ID, true)
	}
	end := q.childRank(parent, ^uint64(0), ^uint64(0), true)
	stop := start + min(min(limit, MaxQueryItems), end-start)
	if err := q.visit(ctx, q.children, start, stop, func(row queryRow) {
		i := row.item
		p.Items = append(p.Items, ChildSummary{ID: i.ID, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Kind: i.Kind, Subtype: i.Subtype, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Headline: childHeadline(i.Summary, i.Kind), DetailOmitted: true})
	}); err != nil {
		return ChildPageResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ChildPageResult{}, err
	}
	p.More, p.Complete = stop < end, stop == end
	return p, nil
}

// childRank counts tuples before the position, including it when inclusive.
func (q *queryIndex) childRank(parent, order, id uint64, inclusive bool) int {
	rank := 0
	for n := q.children; n != nil; {
		q.count(false)
		c := compareQueryKey(parent, order, id, n)
		if c < 0 || c == 0 && !inclusive {
			n = n.left
		} else {
			rank += treeSize(n.left) + 1
			n = n.right
		}
	}
	return rank
}

func childHeadline(summary, kind string) string {
	bounded := func(s string) string {
		s = plainSummary(s[:min(len(s), 256)])
		end := 0
		for at, r := range s {
			size := utf8.RuneLen(r)
			if at+size > 256 {
				break
			}
			end = at + size
		}
		return strings.Clone(s[:end])
	}
	if text := bounded(summary); text != "" {
		return text
	}
	if text := bounded(strings.ReplaceAll(kind[:min(len(kind), 256)], "_", " ")); text != "" {
		return text
	}
	return "item"
}
