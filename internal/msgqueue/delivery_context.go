package msgqueue

import (
	"context"
	"slices"
)

type deliveryMessageKey struct{}

// DeliveryMessage returns the safe queued projection for this write attempt.
// Delivery bytes are deliberately absent. Existing DeliverFunc decorators only
// need to forward their context; callers outside Queue have no metadata.
func DeliveryMessage(ctx context.Context) (QueuedMessage, bool) {
	m, ok := ctx.Value(deliveryMessageKey{}).(QueuedMessage)
	m.AttachmentIDs = slices.Clone(m.AttachmentIDs)
	return m, ok
}

func (m queued) message(sentNow bool) QueuedMessage {
	return QueuedMessage{ID: m.id, MessageID: m.messageID, Text: m.text, TS: m.ts,
		AttachmentIDs: slices.Clone(m.attachmentIDs), DeviceID: m.deviceID, DeviceName: m.deviceName,
		ClientVersion: m.clientVersion, ClientSentAt: m.clientSentAt, SentNow: sentNow}
}

func deliveryContext(ctx context.Context, m queued, sentNow bool) context.Context {
	return context.WithValue(ctx, deliveryMessageKey{}, m.message(sentNow))
}
