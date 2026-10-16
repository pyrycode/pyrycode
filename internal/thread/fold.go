// Package thread folds raw history facts into conversation-owned thread items.
package thread

import (
	"bytes"
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
	EndedOrder           uint64
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
	lastShown                 uint64
	published                 map[uint64]bool
	touches                   map[uint64]uint64
	suppressed                uint64
	unprovenThrough           uint64
	appendedText              int
	publicCounts              *publicWorkCounts
	affected                  []int
	affectedSet               map[int]struct{}
	captureSet                map[int]struct{} // optional Store-owned post-join candidates
	captureAffected           []int
	publicVisible             map[uint64]bool
	items                     []Item
	source                    entrySource
	recipes                   map[uint64]contentRecipe
	successor                 history.SessionProvenance
	lastBoundary, legacyScope uint64
	pending                   map[boundaryKey][]uint64
	turns                     map[mainKey]*mainTurn
	openings                  map[uint64]*mainTurn
	openTexts                 []*mainTurn
	closureCounts             *closureWorkCounts
	ingestCounts              *ingestionWorkCounts
	texts                     map[int]*assistantText
	accepted, messages        map[uint64]int
	childTurns                map[childKey]*mainTurn
	childCalls                map[childCallKey]*childCall
	childReports              map[childCallKey]*mainOutcome
	children                  map[int]*childState
	childCounts               *childWorkCounts
	childEdges                map[childParentKey]map[int]struct{}
	childAgents               map[int]childParentKey
	childDirty                map[int]struct{}
	childEndings              map[int]endingReference
	legacyOutcomes            map[uint64]json.RawMessage
	agentGroups               map[agentKey]*agentGroup
	agentCounts               *agentWorkCounts
	agentLifetimes            map[agentKey]string
	agentObservations         map[uint64][]agentObservation
}

// New creates an independent fold for a caller-established conversation owner.
func New(conversationID string) *Fold {
	return &Fold{
		conversationID:    conversationID,
		texts:             make(map[int]*assistantText),
		published:         make(map[uint64]bool),
		touches:           make(map[uint64]uint64),
		affectedSet:       make(map[int]struct{}),
		publicVisible:     make(map[uint64]bool),
		childTurns:        make(map[childKey]*mainTurn),
		childCalls:        make(map[childCallKey]*childCall),
		childReports:      make(map[childCallKey]*mainOutcome),
		children:          make(map[int]*childState),
		childEdges:        make(map[childParentKey]map[int]struct{}),
		childAgents:       make(map[int]childParentKey),
		childDirty:        make(map[int]struct{}),
		childEndings:      make(map[int]endingReference),
		agentGroups:       make(map[agentKey]*agentGroup),
		agentLifetimes:    make(map[agentKey]string),
		agentObservations: make(map[uint64][]agentObservation),
		pending:           make(map[boundaryKey][]uint64),
		turns:             make(map[mainKey]*mainTurn),
		openings:          make(map[uint64]*mainTurn),
		accepted:          make(map[uint64]int),
		messages:          make(map[uint64]int),
	}
}

// Feed consumes increasing IDs. Unsupported or malformed facts still advance
// version, but cannot affect items, attribution or joins. An invalid ID returns
// an error before consuming that entry; earlier entries remain consumed.
// Reference folds can also fail when resolving a claimed delivery's content.
func (f *Fold) Feed(entries []history.Entry) error {
	for _, e := range entries {
		if f.version == 0 {
			f.countIngestion(24)
		} // one-time fold/map initialization
		f.countIngestion(20) // entry dispatch, identity comparisons and map lookups
		if e.ID == 0 || e.ID > history.MaxEntryID || e.ID <= f.version {
			return errors.New("thread: invalid entry order or id")
		}
		if e.ID != f.version+1 {
			f.unprovenThrough = e.ID - 1
		}
		f.version = e.ID
		var owner struct {
			ConversationID string `json:"conversation_id"`
		}
		if !f.decode(e.Payload, &owner) || (owner.ConversationID != "" && owner.ConversationID != f.conversationID) {
			continue
		}
		f.appendedText = -1
		if err := f.feedEntry(e); err != nil {
			return err
		}
		f.resolveChildren(e.ID)
		f.recordShown()
	}
	return nil
}

func (f *Fold) feedEntry(e history.Entry) error {
	child := f.childWork(e)
	main := false
	if !child {
		main = f.mainWork(e)
	}
	if f.agentWork(e) || main || child {
		return nil
	}
	if handled, err := f.sendFact(e); handled || err != nil {
		return err
	}
	item := Item{ID: e.ID, Order: e.ID, Rev: e.ID, Status: "done", Shown: true}
	if e.Type == "session_divider" || e.Type == protocol.TypeSessionTransition {
		scope := f.legacyScope
		if !f.boundary(e, &item) {
			return nil
		}
		if f.legacyScope != scope {
			f.countIngestion(len(f.agentLifetimes) + 1)
			clear(f.agentLifetimes)
		}
	} else if !standalone(e, &item, f.ingestCounts) {
		return nil
	}
	if item.Kind == "user_message" {
		for len(f.openTexts) > 0 {
			f.countClosureWork(2) // traversal and candidate lookup
			f.closeText(f.openTexts[len(f.openTexts)-1], e.ID)
		}
		f.countClosureWork(1) // final empty check, including messages with no open text
	}
	index := f.addItem(e, item)
	if item.Kind == "user_message" {
		f.messages[e.ID] = index
	}
	return nil
}

func (f *Fold) addItem(e history.Entry, item Item) int {
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
	reference := f.source != nil && referencedKind(item.Kind)
	if reference && (item.Kind == "user_message" || item.Kind == "assistant_message") {
		item.Summary = ""
	} else {
		f.countIngestion(len(item.Summary) * 3)
		item.Summary = plainSummary(item.Summary)
		if item.Summary == "" {
			item.Summary = strings.ReplaceAll(item.Kind, "_", " ")
		}
	}
	if reference {
		r := contentRecipe{creating: e.ID}
		if item.Kind == "assistant_message" {
			r.text = []uint64{e.ID}
		}
		f.recipes[item.ID] = r
	} else {
		if item.Kind == "assistant_message" {
			item.Content = assistantMessageContent(e)
		} else {
			item.Content = append(json.RawMessage(nil), e.Payload...)
		}
		f.countIngestion(len(e.Payload) + len(item.Content))
	}
	f.countIngestion(len(item.Summary)*3 + 1)
	if len(f.items) == cap(f.items) {
		f.countIngestion(len(f.items)*2 + 1)
	}
	f.items = append(f.items, item)
	index := len(f.items) - 1
	f.markAffected(index)
	return index
}

// Version is the newest consumed valid history entry ID.
func (f *Fold) Version() uint64 { return f.version }

// Items returns a creation-ID-ordered snapshot that callers may mutate independently.
// Claimed delivery rows are represented by their permanent acceptance item.
func (f *Fold) Items() []Item {
	result, _ := f.Render() // New folds are inline and cannot fail rendering.
	return result
}

// decode accepts objects only, checks required fields and validates recorded DTO
// fields recursively. Null is valid for pointers, maps and slices, never their scalar
// or struct elements. Unknown fields remain inert source content.
func decode(raw json.RawMessage, dst any, required ...string) bool {
	return (foldDecoder{}).decode(raw, dst, required...)
}

type foldDecoder struct{ counts *ingestionWorkCounts }

func (f *Fold) decode(raw json.RawMessage, dst any, required ...string) bool {
	return (foldDecoder{f.ingestCounts}).decode(raw, dst, required...)
}

func (d foldDecoder) count(n int) {
	if d.counts != nil {
		d.counts.work += n
	}
}

func (d foldDecoder) decode(raw json.RawMessage, dst any, required ...string) bool {
	if objectDTO, empty := dst.(*struct{}); empty && len(required) == 0 {
		// An empty DTO requires object syntax only; unknown fields stay inert.
		// Validate the original bytes so trimming cannot admit non-JSON whitespace.
		d.count(len(raw))
		object := bytes.TrimSpace(raw)
		return objectDTO != nil && len(object) > 0 && object[0] == '{' && json.Valid(raw)
	}
	d.count(2*len(raw) + len(required)) // object and DTO decode bytes, required-field lookups
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
	typ := reflect.TypeOf(dst).Elem()
	if typ.Kind() == reflect.Struct && typ != reflect.TypeOf(time.Time{}) {
		d.count(1) // inspect the root using the fields already decoded above
		return d.validRecordedObject(fields, typ)
	}
	return d.validRecordedFields(raw, typ)
}

// validRecordedObject validates recorded fields from an already decoded object.
// Typed unmarshalling remains responsible for JSON types and custom decoders.
func (d foldDecoder) validRecordedObject(fields map[string]json.RawMessage, typ reflect.Type) bool {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		for key, value := range fields {
			d.count(len(key) + len(name) + 1)
			// Match the case-insensitive field names accepted by encoding/json.
			if strings.EqualFold(key, name) && !d.validRecordedFields(value, field.Type) {
				return false
			}
		}
	}
	return true
}

func (d foldDecoder) validRecordedFields(raw json.RawMessage, typ reflect.Type) bool {
	d.count(len(raw) + 1) // recursive JSON validation bytes and node inspection
	if string(raw) == "null" {
		return typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return d.validRecordedFields(raw, typ.Elem())
	case reflect.Struct:
		// Time's JSON decoder validates its string representation, not struct fields.
		if typ == reflect.TypeOf(time.Time{}) {
			return true
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return false
		}
		return d.validRecordedObject(fields, typ)
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !d.validRecordedFields(value, typ.Elem()) {
				return false
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !d.validRecordedFields(value, typ.Elem()) {
				return false
			}
		}
	}
	return true
}

func standalone(e history.Entry, item *Item, counts ...*ingestionWorkCounts) bool {
	d := foldDecoder{}
	if len(counts) != 0 {
		d.counts = counts[0]
	}
	decode := d.decode
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
