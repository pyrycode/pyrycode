package memorycapture

import "context"

// MessageRevision identifies processed text on one exporter-owned stable row.
// Opaque revisions have no ordering; distinct revisions are independent work.
type MessageRevision struct{ ID, Revision string }

// Receipt acknowledges processing, not that every statement became a fact.
// ChangedPaths includes durable reports from failed attempts as well as success.
// All values are detached from storage and other callers.
type Receipt struct {
	BatchID                           string
	Identity                          Identity
	Processed                         []MessageRevision
	LastTextEntry, LastDeliveredEntry uint64
	ClosingEntry                      *uint64
	ChangedPaths                      []string
}

func validBatch(id Identity, b Batch) bool {
	if !validPayload(b.Payload) || b.ID != batchID(id, b.Payload) {
		return false
	}
	for i, p := range b.ChangedPaths {
		if !validPath(p) || i > 0 && p <= b.ChangedPaths[i-1] {
			return false
		}
	}
	return true
}

func validRecord(r record) bool {
	if r.Version != 1 && r.Version != 2 || r.Version == 1 && (r.Completed != nil || r.Batch.ID == "") {
		return false
	}
	if r.Batch.ID != "" {
		if !validBatch(r.Identity, r.Batch) {
			return false
		}
	} else if digest(r.Batch) != digest(Batch{}) || len(r.Completed) == 0 {
		return false
	}
	seen := make(map[string]bool, len(r.Completed))
	for _, b := range r.Completed {
		if !validBatch(r.Identity, b) || seen[b.ID] || b.ID == r.Batch.ID {
			return false
		}
		seen[b.ID] = true
	}
	return true
}

func receipt(id Identity, b Batch) Receipt {
	r := Receipt{BatchID: b.ID, Identity: id, LastTextEntry: b.Payload.LastTextEntry, LastDeliveredEntry: b.Payload.LastDeliveredEntry, ChangedPaths: append([]string(nil), b.ChangedPaths...)}
	for _, m := range b.Payload.Messages {
		r.Processed = append(r.Processed, MessageRevision{m.ID, m.Revision})
	}
	if b.Payload.ClosingEntry != nil {
		closing := *b.Payload.ClosingEntry
		r.ClosingEntry = &closing
	}
	return r
}

// Acknowledge atomically moves the matching pending batch into durable history.
// The caller must authorize execution success; failures should leave it pending.
// Replay returns the historical receipt without changing newer pending work.
// Errors return no receipt, even if rename committed before a later sync failed.
func (s *Store) Acknowledge(ctx context.Context, id Identity, batch string) (*Receipt, error) {
	var result *Receipt
	err := s.transaction(ctx, id, func(r *record) (*record, error) {
		if r != nil {
			for _, b := range r.Completed {
				if b.ID == batch {
					done := receipt(id, b)
					result = &done
					return nil, nil
				}
			}
		}
		if r == nil || r.Batch.ID == "" || r.Batch.ID != batch {
			return nil, ErrMismatch
		}
		done := receipt(id, r.Batch)
		result = &done
		r.Completed = append(r.Completed, r.Batch)
		r.Version, r.Batch = 2, Batch{}
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Receipts returns acknowledgement-order history for one canonical
// vault/conversation/transcript-generation identity.
func (s *Store) Receipts(ctx context.Context, id Identity) ([]Receipt, error) {
	var result []Receipt
	err := s.transaction(ctx, id, func(r *record) (*record, error) {
		if r != nil {
			for _, b := range r.Completed {
				result = append(result, receipt(id, b))
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func unprocessed(r *record, current []Message) []Message {
	processed := make(map[MessageRevision]bool)
	if r != nil {
		for _, b := range r.Completed {
			for _, m := range b.Payload.Messages {
				processed[MessageRevision{m.ID, m.Revision}] = true
			}
		}
	}
	var result []Message
	for _, m := range current {
		if !processed[MessageRevision{m.ID, m.Revision}] {
			result = append(result, m)
		}
	}
	return result
}

// Unprocessed selects unacknowledged identity/revision pairs from the current
// caller-supplied eligible snapshot, preserving order and attribution.
func (s *Store) Unprocessed(ctx context.Context, id Identity, current []Message) ([]Message, error) {
	if !validPayload(Payload{Agent: "query", Model: "query", Messages: current}) {
		return nil, ErrInvalid
	}
	var result []Message
	err := s.transaction(ctx, id, func(r *record) (*record, error) {
		result = unprocessed(r, current)
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Complete requires the current closure acknowledged, all current eligible
// revisions processed, and no pending batch. An open transcript is never final.
// Later eligible text or an edited row invalidates completion without changing
// historical receipts or the exporter-owned closure marker.
func (s *Store) Complete(ctx context.Context, id Identity, current []Message, closure *uint64) (bool, error) {
	if !validPayload(Payload{Agent: "query", Model: "query", Messages: current}) {
		return false, ErrInvalid
	}
	var result bool
	err := s.transaction(ctx, id, func(r *record) (*record, error) {
		if r != nil && closure != nil && r.Batch.ID == "" && len(unprocessed(r, current)) == 0 {
			for _, b := range r.Completed {
				if b.Payload.ClosingEntry != nil && *b.Payload.ClosingEntry == *closure {
					result = true
					break
				}
			}
		}
		return nil, nil
	})
	if err != nil {
		return false, err
	}
	return result, nil
}
