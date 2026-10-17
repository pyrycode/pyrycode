package thread

import (
	"cmp"
	"context"
	"maps"
)

type queryRow struct {
	item         *Item
	touch        uint64
	recipe       contentRecipe
	header       childHeader
	activeBranch bool // own activity or activity in a public descendant
}

// queryTree is a persistent AVL tree ordered by (group, key, item ID). Subtree sizes
// support bounded rank selection; updates never mutate a borrowed node.
type queryTree struct {
	left, right  *queryTree
	row          queryRow
	key          uint64
	group        uint64
	height, size int
}

func treeHeight(n *queryTree) int {
	if n == nil {
		return 0
	}
	return n.height
}
func treeSize(n *queryTree) int {
	if n == nil {
		return 0
	}
	return n.size
}
func (n *queryTree) fix() {
	n.height = 1 + max(treeHeight(n.left), treeHeight(n.right))
	n.size = 1 + treeSize(n.left) + treeSize(n.right)
}
func (q *queryIndex) rotate(n *queryTree, left bool) *queryTree {
	q.count(true)
	if q.copied != nil {
		q.copied(5)
	}
	var root queryTree
	if !left {
		root = *n.left
		n.left, root.right = root.right, n
	} else {
		root = *n.right
		n.right, root.left = root.left, n
	}
	n.fix()
	root.fix()
	return &root
}

// treePut inserts/replaces row, or deletes (key,id) when row.item is nil.
func (q *queryIndex) treePut(n *queryTree, key, id uint64, row queryRow) *queryTree {
	return q.treePutGroup(n, 0, key, id, row)
}

func compareQueryKey(group, key, id uint64, n *queryTree) int {
	if c := cmp.Compare(group, n.group); c != 0 {
		return c
	}
	if c := cmp.Compare(key, n.key); c != 0 {
		return c
	}
	return cmp.Compare(id, n.row.item.ID)
}

func (q *queryIndex) treePutGroup(n *queryTree, group, key, id uint64, row queryRow) *queryTree {
	q.count(true)
	if n == nil {
		if row.item == nil {
			return nil
		}
		return &queryTree{group: group, key: key, row: row, height: 1, size: 1}
	}
	c := *n
	if q.copied != nil {
		q.copied(5)
	}
	comparison := compareQueryKey(group, key, id, n)
	if comparison < 0 {
		c.left = q.treePutGroup(n.left, group, key, id, row)
	} else if comparison > 0 {
		c.right = q.treePutGroup(n.right, group, key, id, row)
	} else if row.item != nil {
		c.row = row
	} else {
		if n.left == nil {
			return n.right
		}
		if n.right == nil {
			return n.left
		}
		next := n.right
		for next.left != nil {
			q.count(true)
			next = next.left
		}
		c.group, c.key, c.row = next.group, next.key, next.row
		c.right = q.treePutGroup(n.right, next.group, next.key, next.row.item.ID, queryRow{})
	}
	c.fix()
	if treeHeight(c.left)-treeHeight(c.right) > 1 {
		if treeHeight(c.left.right) > treeHeight(c.left.left) {
			child := *c.left
			if q.copied != nil {
				q.copied(5)
			}
			c.left = q.rotate(&child, true)
		}
		return q.rotate(&c, false)
	}
	if treeHeight(c.right)-treeHeight(c.left) > 1 {
		if treeHeight(c.right.left) > treeHeight(c.right.right) {
			child := *c.right
			if q.copied != nil {
				q.copied(5)
			}
			c.right = q.rotate(&child, false)
		}
		return q.rotate(&c, true)
	}
	return &c
}

func (q *queryIndex) lookup(id uint64, preparing bool) (queryRow, bool) {
	q.count(preparing)
	row, ok := q.byID[id%uint64(len(q.byID))][id]
	return row, ok
}

func (q *queryIndex) rank(n *queryTree, key uint64) int {
	if n == nil {
		return 0
	}
	q.count(false)
	if n.key >= key {
		return q.rank(n.left, key)
	}
	return treeSize(n.left) + 1 + q.rank(n.right, key)
}

func (q *queryIndex) at(n *queryTree, rank int) queryRow {
	for n != nil {
		q.count(false)
		size := treeSize(n.left)
		if rank == size {
			return n.row
		}
		if rank < size {
			n = n.left
		} else {
			rank -= size + 1
			n = n.right
		}
	}
	return queryRow{}
}

func (q *queryIndex) visit(ctx context.Context, n *queryTree, start, end int, add func(queryRow)) error {
	if n == nil || start >= end {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	q.count(false)
	size := treeSize(n.left)
	if err := q.visit(ctx, n.left, start, min(end, size), add); err != nil {
		return err
	}
	if start <= size && size < end {
		add(n.row)
	}
	return q.visit(ctx, n.right, max(0, start-size-1), end-size-1, add)
}

func (q *queryIndex) membership(row queryRow, remove bool) {
	item := row.item
	value := row
	if remove {
		value = queryRow{}
	}
	bucket := item.ID % uint64(len(q.byID))
	q.count(true)
	if remove {
		delete(q.byID[bucket], item.ID)
	} else {
		if q.byID[bucket] == nil {
			q.byID[bucket] = make(map[uint64]queryRow)
		}
		q.byID[bucket][item.ID] = row
	}
	if item.Order != 0 {
		q.ordered = q.treePut(q.ordered, item.Order, item.ID, value)
	}
	if item.Active {
		q.active = q.treePut(q.active, item.ID, item.ID, value)
	}
	q.touched = q.treePut(q.touched, row.touch, item.ID, value)
	q.children = q.treePutGroup(q.children, item.Parent, item.Order, item.ID, value)
	q.compactMembership(row, value)
	if row.touch == 0 {
		if remove {
			q.invalidTouches--
		} else {
			q.invalidTouches++
		}
	}
}

func (q *queryIndex) advance(items []*Item, affected []int, f *Fold, work func(bool), copied func(int), headers map[uint64]childHeader) *queryIndex {
	next := *q
	next.work = work
	next.copied = copied
	// Copy only affected hash buckets. Clone copies index storage without
	// comparing unrelated items; parent lookup remains constant-time.
	var cloned [256]bool
	cloneBucket := func(id uint64) {
		bucket := id % uint64(len(next.byID))
		if !cloned[bucket] {
			if copied != nil {
				copied(5 * len(next.byID[bucket]))
			}
			next.byID[bucket] = maps.Clone(next.byID[bucket])
			cloned[bucket] = true
		}
	}
	dirty := make(map[uint64]bool)
	ancestors := func(index *queryIndex, id uint64) {
		for id != 0 && !dirty[id] {
			next.count(true)
			row, ok := index.byID[id%uint64(len(index.byID))][id]
			if !ok {
				break
			}
			dirty[id] = true
			id = row.item.Parent
		}
	}
	for _, i := range affected {
		ancestors(q, f.items[i].ID)
	}
	if copied != nil {
		copied(len(next.byID))
	}
	for _, i := range affected {
		next.count(true)
		cloneBucket(f.items[i].ID)
		if old, ok := next.lookup(f.items[i].ID, true); ok {
			next.membership(old, true)
		}
		if items[i] != nil {
			next.membership(queryRow{item: items[i], touch: f.touches[items[i].ID], recipe: f.recipes[items[i].ID], header: headers[items[i].ID]}, false)
		}
	}
	// Walk the new chains independently: a reparented row may already be dirty
	// from its old chain, while its new ancestors still need invalidation.
	oldDirty := dirty
	dirty = make(map[uint64]bool)
	for _, i := range affected {
		ancestors(&next, f.items[i].ID)
	}
	for id := range oldDirty {
		dirty[id] = true
	}
	for id := range dirty {
		if row, ok := next.lookup(id, true); ok {
			cloneBucket(id)
			next.refreshHeader(row, f.version)
		}
	}
	next.refreshRootActivity(dirty)
	next.evidence(f)
	return &next
}

func (q *queryIndex) evidence(f *Fold) {
	q.coveredThrough, q.unprovenThrough, q.suppressed = f.version, f.unprovenThrough, f.suppressed
	q.complete = q.invalidTouches == 0
}
