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

// Resource payloads are data-only. Consumers validate required presence, exact
// JSON types, safe integers, URL/header validity, byte bounds and authorization.
// Hosted envelopes omit conversation/session/replay metadata and payload_encrypted.

// AppAssetRequestPayload requests a pinned public asset with GET semantics.
// Navigation is native-stamped; false distinguishes subresources from SPA loads.
// Path and Query preserve escaped URL bytes; an empty Query is omitted.
type AppAssetRequestPayload struct {
	AppID          string `json:"app_id"`
	ReleaseVersion string `json:"release_version"`
	Path           string `json:"path"`
	Navigation     bool   `json:"navigation"`
	Query          string `json:"query,omitempty"`
}

// AppAPIRequestPayload requests a pinned service API. Headers map permitted
// lowercase names to strings. Body is standard padded base64 of opaque bytes,
// including the required empty string for no body, with at most 32768 decoded bytes.
type AppAPIRequestPayload struct {
	AppID          string            `json:"app_id"`
	ReleaseVersion string            `json:"release_version"`
	Path           string            `json:"path"`
	Query          string            `json:"query,omitempty"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	Body           string            `json:"body"`
}

// MarshalJSON emits the required header object even when Headers is nil.
func (p AppAPIRequestPayload) MarshalJSON() ([]byte, error) {
	type wire AppAPIRequestPayload
	if p.Headers == nil {
		p.Headers = map[string]string{}
	}
	return json.Marshal(wire(p))
}

// AppResponsePayload supplies metadata once, with Envelope.InReplyTo naming the
// original asset/API request. Completion requires the exact decoded SizeBytes,
// TotalChunks and lowercase whole-body SHA256, including the empty-body digest.
// Assets allow 8388608 bytes/256 chunks; APIs allow 1048576 bytes/32 chunks.
type AppResponsePayload struct {
	AppID          string            `json:"app_id"`
	ReleaseVersion string            `json:"release_version"`
	Status         int               `json:"status"`
	Headers        map[string]string `json:"headers"`
	SizeBytes      uint64            `json:"size_bytes"`
	TotalChunks    uint64            `json:"total_chunks"`
	SHA256         string            `json:"sha256"`
}

// MarshalJSON emits the required header object even when Headers is nil.
func (p AppResponsePayload) MarshalJSON() ([]byte, error) {
	type wire AppResponsePayload
	if p.Headers == nil {
		p.Headers = map[string]string{}
	}
	return json.Marshal(wire(p))
}

// AppResponseChunkPayload carries 1..32768 opaque bytes as standard padded base64.
// Index is consecutive from zero; Envelope.InReplyTo names the original resource
// request, never the response metadata ID. Producers bound final envelopes to 65519 bytes.
type AppResponseChunkPayload struct {
	AppID string `json:"app_id"`
	Index uint64 `json:"index"`
	Data  string `json:"data"`
}

// AppResponseCreditPayload grants one next chunk of the original resource request.
// RequestID is in 0..9007199254740991; NextIndex is the expected index in 0..255.
// Credit carries no Envelope.InReplyTo and receives no reply.
type AppResponseCreditPayload struct {
	AppID     string `json:"app_id"`
	RequestID uint64 `json:"request_id"`
	NextIndex uint64 `json:"next_index"`
}
