package protocol

import (
	"encoding/json"
	"reflect"
)

// EncodeThreadToolBody encodes an available detached body and separate marker.
// Each envelope fits MaxThreadEnvelopeBytes with threadStampBudget reservations;
// supply variable ConversationID/SessionID and InReplyTo before encoding as in
// EncodeThreadReply. Missing/private/unavailable/wrong-kind/stale reads must use
// the existing correlated, content-free thread.query_failed refusal instead.
// Errors return nil output; content remains inert and inputs are never modified.
func EncodeThreadToolBody(env Envelope, r ThreadExpansionRequest, p ThreadToolBodyPayload) ([][]byte, error) {
	if env.InReplyTo == nil || r.Cursor != nil || !threadExpansionMatches(r, p.ConversationID, p.Epoch, p.ItemID, p.Rev) || (p.Kind != "tool_call" && p.Kind != "agent") || p.Final {
		return nil, ErrInvalidThreadUpdate
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(p.Content, &content) != nil || content == nil {
		return nil, ErrInvalidThreadUpdate
	}
	env.Type = TypeThreadToolBody
	marker := p
	marker.Content, marker.Final = nil, true
	final, err := threadEncodePayload(env, marker, nil)
	if err != nil {
		return nil, err
	}
	part := ThreadUpdatePart{ConversationID: p.ConversationID, Epoch: p.Epoch, Version: p.Version, ItemID: p.ItemID, Rev: p.Rev}
	data, err := threadEncodePayload(env, p, &part)
	if err != nil {
		return nil, err
	}
	return append(data, final...), nil
}

func threadExpansionMatches(r ThreadExpansionRequest, conversation, epoch string, id, rev uint64) bool {
	return r.ConversationID == conversation && r.Epoch == epoch && r.ItemID == id && r.ExpectedRev == rev
}

// EncodeThreadChildren packs an ascending detached candidate prefix. Candidates
// must be public direct children, strictly ordered by (order,id), after Cursor.
// remaining is authoritative evidence of rows beyond the supplied candidates.
// The normalized limit is a hard ceiling including ties. No extras are added.
// All envelopes together, including marker and reserved stamps, fit the shared
// cap. Stop before the first row that does not fit, without skipping it. If rows
// remain but none fits, refuse without completing or advancing. Any encoding
// failure returns nil, including invalid candidates beyond the selected prefix.
// Envelope metadata, input ownership and refusal rules match EncodeThreadToolBody.
func EncodeThreadChildren(env Envelope, r ThreadExpansionRequest, p ThreadChildrenPayload, remaining bool) ([][]byte, error) {
	limit, err := NormalizeThreadQueryLimit(r.Limit)
	if err != nil {
		return nil, err
	}
	if env.InReplyTo == nil || !threadExpansionMatches(r, p.ConversationID, p.Epoch, p.ItemID, p.Rev) || (r.Cursor != nil && !r.Cursor.Matches(r, p.Version)) || !threadValidUTF8(reflect.ValueOf(p)) {
		return nil, ErrInvalidThreadUpdate
	}
	p.Children = append([]ThreadSummary{}, p.Children...)
	var order, id uint64
	if r.Cursor != nil {
		order, id = r.Cursor.Order, r.Cursor.ID
	}
	for i := range p.Children {
		row := &p.Children[i]
		if row.Parent != p.ItemID || ((i > 0 || r.Cursor != nil) && (row.Order < order || (row.Order == order && row.ID <= id))) {
			return nil, ErrInvalidThreadUpdate
		}
		order, id = row.Order, row.ID
		row.Headline, row.DetailOmitted = threadHeadline(row.Headline), true
	}
	env.Type = TypeThreadChildren
	var best [][]byte
	for n := 0; n <= min(limit, len(p.Children)); n++ {
		data := p
		data.Children, data.Final, data.More, data.Cursor = p.Children[:n], false, nil, nil
		more := remaining || n < len(p.Children)
		marker := data
		marker.Children, marker.Final, marker.More = []ThreadSummary{}, true, &more
		if more && n > 0 {
			last := p.Children[n-1]
			marker.Cursor = &ThreadExpansionCursor{ConversationID: p.ConversationID, Epoch: p.Epoch, ItemID: p.ItemID, Rev: p.Rev, Version: p.Version, Order: last.Order, ID: last.ID}
		}
		final, err := threadEncodePayload(env, marker, nil)
		if err != nil {
			return nil, err
		}
		seq := final
		if n > 0 {
			batch, err := threadEncodePayload(env, data, nil)
			if err != nil {
				if err == ErrThreadUpdateMetadataTooLarge {
					break
				}
				return nil, err
			}
			seq = append(batch, final...)
		}
		total := 0
		for _, raw := range seq {
			var e Envelope
			if json.Unmarshal(raw, &e) != nil {
				return nil, ErrInvalidThreadUpdate
			}
			stamped, err := json.Marshal(threadStampBudget(e))
			if err != nil {
				return nil, ErrInvalidThreadUpdate
			}
			total += len(stamped)
		}
		if total > MaxThreadEnvelopeBytes {
			break
		}
		if n > 0 || !more {
			best = seq
		}
	}
	if best == nil {
		return nil, ErrThreadUpdateMetadataTooLarge
	}
	return best, nil
}
