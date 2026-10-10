package protocol

import "encoding/json"

// CapabilityHostedAppsV1 names the hosted-app contract. It is declaration-only:
// support requires registration, supervision and resource routing before advertisement.
const CapabilityHostedAppsV1 = "hosted_apps_v1"

// HostedAppRecord is one discovery item and the whole app_updated payload.
// Updates are unsolicited and omit Envelope.InReplyTo and session/replay metadata.
// Release and error keys are required even when null. LastError contains only
// a safe code and static message, unlike the bridge's separate ErrorPayload.
// Consumers validate identity, state, required keys and revisions in 1..9007199254740991.
type HostedAppRecord struct {
	AppID          string  `json:"app_id"`
	Title          string  `json:"title"`
	Desired        string  `json:"desired"`
	State          string  `json:"state"`
	ActiveRelease  *string `json:"active_release"`
	PendingRelease *string `json:"pending_release"`
	LastError      *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"last_error"`
	Revision uint64 `json:"revision"`
}

// ListAppsPayload is a client-to-daemon discovery request. The first page is {};
// a subsequent page carries a nonempty opaque cursor validated by the handler.
type ListAppsPayload struct {
	Cursor string `json:"cursor,omitempty"`
}

// AppsPayload replies to the list ID through Envelope.InReplyTo. Revision is
// the host page sequence in 0..9007199254740991. Items is always an array;
// NextCursor is required and null at the end of the walk.
type AppsPayload struct {
	Revision   uint64            `json:"revision"`
	Items      []HostedAppRecord `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

// MarshalJSON emits an empty array for a page without items.
func (p AppsPayload) MarshalJSON() ([]byte, error) {
	type wire AppsPayload
	if p.Items == nil {
		p.Items = []HostedAppRecord{}
	}
	return json.Marshal(wire(p))
}

// AppRemovedPayload is an unsolicited host-scoped tombstone. Revision is in
// 1..9007199254740991. Removal does not assert deletion of saved app data.
type AppRemovedPayload struct {
	AppID    string `json:"app_id"`
	Revision uint64 `json:"revision"`
}

// AppCancelPayload is shared by client app_cancel and daemon app_cancelled.
// AppID is omitted for list work and required for asset/API work in both directions.
// RequestID is the original target in 0..9007199254740991; the acknowledgement's
// Envelope.InReplyTo names the cancel ID and does not assert mutation rollback.
type AppCancelPayload struct {
	AppID     string `json:"app_id,omitempty"`
	RequestID uint64 `json:"request_id"`
}
