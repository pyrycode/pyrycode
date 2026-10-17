package protocol

import (
	"encoding/json"
	"strings"
	"unicode"
)

// ThreadExpansionRequest names a tool body or a direct-child page of ItemID.
// First reads require matching nonempty Epoch and ExpectedRev. Children use Limit
// and an optional exclusive Cursor. Handlers enforce required fields, public
// ownership and normal thread/interactive authorization, not DTO decoding.
type ThreadExpansionRequest struct {
	ConversationID string                 `json:"conversation_id"`
	Epoch          string                 `json:"epoch"`
	ItemID         uint64                 `json:"item_id"`
	ExpectedRev    uint64                 `json:"expected_rev"`
	Limit          int                    `json:"limit,omitempty"`
	Cursor         *ThreadExpansionCursor `json:"cursor,omitempty"`
}

// ThreadExpansionCursor binds a child enumeration to one capture and its last
// emitted (Order, ID). Resume strictly after that tuple, including order-zero
// rows. Any binding mismatch requires a fresh first page, not an old snapshot.
// The cursor conveys no authority and must be checked against current Store facts.
type ThreadExpansionCursor struct {
	ConversationID string `json:"conversation_id"`
	Epoch          string `json:"epoch"`
	ItemID         uint64 `json:"item_id"`
	Rev            uint64 `json:"rev"`
	Version        uint64 `json:"version"`
	Order          uint64 `json:"order"`
	ID             uint64 `json:"id"`
}

// Matches compares scope, expected parent revision, epoch and consumed version.
func (c ThreadExpansionCursor) Matches(r ThreadExpansionRequest, version uint64) bool {
	return c.ConversationID == r.ConversationID && c.Epoch == r.Epoch && c.ItemID == r.ItemID && c.Rev == r.ExpectedRev && c.Version == version
}

// ThreadSummary contains metadata only, with explicitly omitted detail and no
// descendant inventory. Headline is plain single-line display text, at most 200
// Unicode code points including an ellipsis. Never interpret it as markup.
type ThreadSummary struct {
	ID            uint64 `json:"id"`
	Kind          string `json:"kind"`
	Order         uint64 `json:"order"`
	Rev           uint64 `json:"rev"`
	EndedOrder    uint64 `json:"ended_order,omitempty"`
	Session       string `json:"session,omitempty"`
	Agent         string `json:"agent,omitempty"`
	NoChild       bool   `json:"no_child,omitempty"`
	Turn          string `json:"turn,omitempty"`
	Parent        uint64 `json:"parent"`
	Status        string `json:"status"`
	Active        bool   `json:"active"`
	Shown         bool   `json:"shown"`
	Headline      string `json:"headline"`
	Subtype       string `json:"subtype,omitempty"`
	DetailOmitted bool   `json:"detail_omitted"`
}

// NewThreadSummary projects metadata without retaining Content.
func NewThreadSummary(i ThreadItem) ThreadSummary {
	return ThreadSummary{ID: i.ID, Kind: i.Kind, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Headline: threadHeadline(i.Summary), Subtype: i.Subtype, DetailOmitted: true}
}

func threadHeadline(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	r := []rune(s)
	if len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

// ThreadToolBodyPayload carries existing rendered tool_call/agent Content,
// including unknown fields, without additional truncation or enlarged upstream
// limits. An available empty input/output object differs from absent detail.
// A separate Final marker omits Content. Fragment final completes assembly only;
// complete data plus the marker completes this request at this capture.
type ThreadToolBodyPayload struct {
	ConversationID string          `json:"conversation_id"`
	Epoch          string          `json:"epoch"`
	Version        uint64          `json:"version"`
	ItemID         uint64          `json:"item_id"`
	Rev            uint64          `json:"rev"`
	Kind           string          `json:"kind"`
	Content        json.RawMessage `json:"content,omitempty"`
	Final          bool            `json:"final"`
}

// ThreadChildrenPayload carries direct summaries or a separate empty Final
// marker. Only the marker has More and (when true) Cursor. More is authoritative;
// only false finishes the parent's enumeration at this capture. Interrupted,
// cancelled or failed delivery certifies nothing. Neither expansion completion
// nor its Version advances full-thread checkpoints or live join watermarks.
type ThreadChildrenPayload struct {
	ConversationID string                 `json:"conversation_id"`
	Epoch          string                 `json:"epoch"`
	Version        uint64                 `json:"version"`
	ItemID         uint64                 `json:"item_id"`
	Rev            uint64                 `json:"rev"`
	Children       []ThreadSummary        `json:"children"`
	Final          bool                   `json:"final"`
	More           *bool                  `json:"more,omitempty"`
	Cursor         *ThreadExpansionCursor `json:"cursor,omitempty"`
}
