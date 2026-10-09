// Package thread folds raw history facts into conversation-owned thread items.
package thread

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Item is a stable row. Content retains the source kind's saved JSON fields as
// inert data; NoChild distinguishes explicit no-child provenance from unknown.
type Item struct {
	ID, Order, Rev       uint64
	Kind, Session, Agent string
	NoChild              bool
	Turn                 string
	Parent               uint64
	Status               string
	Active, Shown        bool
	Summary, Subtype     string
	Content              json.RawMessage
}

// Fold belongs to one conversation. Its owner must serialize all access.
// The zero value is not bound to a conversation; construct folds with New.
type Fold struct {
	conversationID            string
	version                   uint64
	items                     []Item
	successor                 history.SessionProvenance
	lastBoundary, legacyScope uint64
	pending                   map[boundaryKey][]uint64
}

// New creates an independent fold for a caller-established conversation owner.
func New(conversationID string) *Fold {
	return &Fold{conversationID: conversationID, pending: make(map[boundaryKey][]uint64)}
}

// Feed consumes increasing IDs. Unsupported or malformed facts still advance
// version, but cannot affect items, attribution or joins. An invalid ID returns
// an error before consuming that entry; earlier entries remain consumed.
func (f *Fold) Feed(entries []history.Entry) error {
	for _, e := range entries {
		if e.ID == 0 || e.ID > history.MaxEntryID || e.ID <= f.version {
			return errors.New("thread: invalid entry order or id")
		}
		f.version = e.ID
		var owner struct {
			ConversationID string `json:"conversation_id"`
		}
		if !decode(e.Payload, &owner) || (owner.ConversationID != "" && owner.ConversationID != f.conversationID) {
			continue
		}
		item := Item{ID: e.ID, Order: e.ID, Rev: e.ID, Status: "done", Shown: true}
		if e.Type == "session_divider" || e.Type == protocol.TypeSessionTransition {
			if !f.boundary(e, &item) {
				continue
			}
		} else if !standalone(e, &item) {
			continue
		}
		source := f.successor
		if item.Session != "" || e.Type == "session_divider" || e.Type == protocol.TypeSessionTransition {
			source = history.SessionProvenance{SessionID: item.Session, Kind: item.Agent}
		}
		if e.Session != nil {
			source = *e.Session
		}
		item.Session = source.SessionID
		item.Agent = recordedAgent(source.Kind)
		item.NoChild = source.Kind == "none"
		if e.Shown != nil {
			item.Shown = *e.Shown
		}
		item.Summary = plainSummary(item.Summary)
		if item.Summary == "" {
			item.Summary = strings.ReplaceAll(item.Kind, "_", " ")
		}
		item.Content = append(json.RawMessage(nil), e.Payload...)
		f.items = append(f.items, item)
	}
	return nil
}

// Version is the newest consumed valid history entry ID.
func (f *Fold) Version() uint64 { return f.version }

// Items returns an ordered snapshot that callers may mutate independently.
func (f *Fold) Items() []Item {
	result := append([]Item(nil), f.items...)
	for i := range result {
		result[i].Content = append(json.RawMessage(nil), result[i].Content...)
	}
	return result
}

// decode accepts objects only, checks required fields and validates recorded DTO
// fields recursively. Null is valid for pointers and slices, never their scalar
// or struct elements. Unknown fields remain inert source content.
func decode(raw json.RawMessage, dst any, required ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, dst) != nil {
		return false
	}
	for _, key := range required {
		v, ok := fields[key]
		if !ok || string(v) == "null" {
			return false
		}
	}
	return validRecordedFields(raw, reflect.TypeOf(dst).Elem())
}

func validRecordedFields(raw json.RawMessage, typ reflect.Type) bool {
	if string(raw) == "null" {
		return typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return validRecordedFields(raw, typ.Elem())
	case reflect.Struct:
		// Time's JSON decoder validates its string representation, not struct fields.
		if typ == reflect.TypeOf(time.Time{}) {
			return true
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return false
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			for key, value := range fields {
				// Match the case-insensitive field names accepted by encoding/json.
				if strings.EqualFold(key, name) && !validRecordedFields(value, field.Type) {
					return false
				}
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !validRecordedFields(value, typ.Elem()) {
				return false
			}
		}
	}
	return true
}

func standalone(e history.Entry, item *Item) bool {
	item.Kind = "notice"
	item.Subtype = e.Type
	switch e.Type {
	case protocol.TypeMessage:
		var p protocol.MessagePayload
		if !decode(e.Payload, &p, "role", "text") || p.Role != "user" {
			return false
		}
		item.Kind = "user_message"
		item.Subtype = ""
		item.Status = "delivered"
		item.Summary = p.Text
	case protocol.TypeCompactionBoundary:
		var p protocol.CompactionBoundaryPayload
		if !decode(e.Payload, &p, "trigger") {
			return false
		}
		item.Kind = "compaction"
		item.Subtype = ""
		item.Summary = "Context compacted"
	case protocol.TypeBanner:
		var p protocol.BannerPayload
		if !decode(e.Payload, &p, "text") {
			return false
		}
		item.Summary = p.Text
		item.Shown = p.Level != "info" || p.StopsTurn
	case protocol.TypeModelRefusalFallback:
		var p protocol.ModelRefusalFallbackPayload
		if !decode(e.Payload, &p, "banner") {
			return false
		}
		item.Summary = p.Banner
	case protocol.TypeModelRefusalNoFallback:
		var p protocol.ModelRefusalNoFallbackPayload
		if !decode(e.Payload, &p, "banner") {
			return false
		}
		item.Summary = p.Banner
	case protocol.TypeUnrecognizedMessage:
		var p protocol.UnrecognizedMessagePayload
		if !decode(e.Payload, &p, "raw") {
			return false
		}
		item.Summary = "Unrecognized message"
	case protocol.TypeAttachmentOffered:
		var p protocol.AttachmentOfferedPayload
		if !decode(e.Payload, &p, "attachment_id", "filename") {
			return false
		}
		item.Summary = p.Filename
		item.Shown = false
	case "prompt_answered":
		var p promptAnswer
		if !decode(e.Payload, &p, "correlation_id", "decision", "context") {
			return false
		}
		item.Summary = "Prompt answered: " + p.Decision
		item.Session = p.SessionID
	default:
		return false
	}
	return true
}

type promptAnswer struct {
	ConversationID string    `json:"conversation_id"`
	CorrelationID  string    `json:"correlation_id"`
	SessionID      string    `json:"session_id"`
	ResolvedAt     time.Time `json:"resolved_at"`
	Source         string    `json:"source"`
	Decision       string    `json:"decision"`
	Behavior       string    `json:"behavior"`
	SessionGrant   bool      `json:"session_grant"`
	Truncated      bool      `json:"truncated"`
	Context        struct {
		Tool      string `json:"tool"`
		Class     string `json:"class"`
		Questions []struct {
			Index       int    `json:"index"`
			Text        string `json:"text"`
			MultiSelect bool   `json:"multi_select"`
			Values      []struct {
				Text    string `json:"text"`
				Meaning string `json:"meaning"`
			} `json:"values"`
		} `json:"questions"`
	} `json:"context"`
}

func recordedAgent(kind string) string {
	if kind == "claude" || kind == "codex" {
		return kind
	}
	return ""
}
func plainSummary(text string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)), " ")
}
