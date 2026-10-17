package protocol

import "errors"

// EncodeThreadReply encodes a detached complete query result or terminal failure.
// Supply a ThreadItemsPayload with Final true, all supplied items and exactly one
// coverage shape, or a ThreadResetPayload/ErrorPayload. No window limit is applied.
// Items are batched with Final false, followed by one separate empty final marker.
// Oversized single-item batches reuse ThreadUpdatePart under TypeThreadItems.
// Every frame fits the shared reserved stamp budget; terminal frames never fragment.
// Any error returns nil, never a usable prefix. Content is inert and errors are
// content-free. Supply InReplyTo and variable ConversationID/SessionID before
// encoding; preserve them afterwards. Other stamping follows EncodeThreadUpdate.
// Callers own authorization, semantic item validation and ordered delivery.
func EncodeThreadReply(env Envelope, reply any) ([][]byte, error) {
	if env.InReplyTo == nil {
		return nil, ErrInvalidThreadUpdate
	}
	switch p := reply.(type) {
	case ThreadResetPayload:
		if !threadProjectionScope(p.Projection, p.ConversationID, p.Epoch) {
			return nil, ErrInvalidThreadUpdate
		}
		env.Type = TypeThreadReset
		return threadEncodePayload(env, p, nil)
	case ErrorPayload:
		env.Type = TypeError
		return threadEncodePayload(env, p, nil)
	case ThreadItemsPayload:
		env.Type = TypeThreadItems
		if !p.Final || !threadQueryCoverage(p) || !threadProjectionScope(p.Projection, p.ConversationID, p.Epoch) || (p.Projection == "" && len(p.Headers) > 0) {
			return nil, ErrInvalidThreadUpdate
		}
		marker := p
		marker.Items = []ThreadItem{}
		marker.Headers = nil
		final, err := threadEncodePayload(env, marker, nil)
		if err != nil {
			return nil, err
		}
		batch := ThreadItemsPayload{ConversationID: p.ConversationID, Epoch: p.Epoch, Version: p.Version, Projection: p.Projection}
		var result, pending [][]byte
		for index := 0; index < len(p.Items)+len(p.Headers); index++ {
			appendRow := func() (uint64, uint64) {
				if index < len(p.Items) {
					item := p.Items[index]
					batch.Items = append(batch.Items, item)
					return item.ID, item.Rev
				}
				header := p.Headers[index-len(p.Items)]
				batch.Headers = append(batch.Headers, header)
				return header.ID, header.Rev
			}
			id, rev := appendRow()
			encoded, err := threadEncodePayload(env, batch, nil)
			if errors.Is(err, ErrThreadUpdateMetadataTooLarge) {
				result = append(result, pending...)
				batch.Items, batch.Headers = nil, nil
				appendRow()
				part := ThreadUpdatePart{Projection: p.Projection, ConversationID: p.ConversationID, Epoch: p.Epoch, Version: p.Version, ItemID: id, Rev: rev}
				encoded, err = threadEncodePayload(env, batch, &part)
			}
			if err != nil {
				return nil, err
			}
			if len(encoded) > 1 {
				result = append(result, encoded...)
				pending = nil
				batch.Items = nil
				batch.Headers = nil
			} else {
				pending = encoded
			}
		}
		result = append(result, pending...)
		return append(result, final...), nil
	default:
		return nil, ErrInvalidThreadUpdate
	}
}

func threadProjectionScope(projection, conversation, epoch string) bool {
	return projection == "" || (projection == ThreadProjectionCompact && conversation != "" && epoch != "")
}

func threadQueryCoverage(p ThreadItemsPayload) bool {
	page := p.LowerOrder != nil && p.UpperOrder != nil && p.Continuation != nil && p.OlderExists != nil
	if p.FromVersion != nil {
		return *p.FromVersion > 0 && *p.FromVersion <= p.Version && p.LowerOrder == nil && p.UpperOrder == nil && p.Continuation == nil && p.OlderExists == nil
	}
	return page && *p.Continuation == *p.LowerOrder && *p.LowerOrder <= *p.UpperOrder
}
