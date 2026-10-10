package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"
	"unicode/utf8"
)

// MaxThreadEnvelopeBytes is the encrypted application plaintext cap.
const MaxThreadEnvelopeBytes = 65519

var (
	// ErrInvalidThreadUpdate rejects invalid encoding inputs without exposing data.
	ErrInvalidThreadUpdate = errors.New("protocol: invalid thread update")
	// ErrThreadUpdateMetadataTooLarge reports insufficient repeated metadata budget.
	ErrThreadUpdateMetadataTooLarge = errors.New("protocol: thread update metadata leaves no fragment room")
)

// ThreadContinuation describes assembly progress, never history progress.
// UpdateID is SHA-256(type + NUL + serialized logical payload), in lowercase hex.
// Index and Offset start at zero. TotalBytes counts decoded Data bytes.
// Consumers require consecutive indices/offsets and identical assembly metadata;
// Final commits only after length, digest, routing and base_rev validation.
// A failed assembly requires catch-up without advancing completed rev/version.
type ThreadContinuation struct {
	UpdateID   string `json:"update_id"`
	Index      uint64 `json:"index"`
	Offset     uint64 `json:"offset"`
	TotalBytes uint64 `json:"total_bytes"`
	Final      bool   `json:"final"`
}

// ThreadUpdatePart replaces an oversized update payload under its original type.
// Check Continuation before decoding an ordinary DTO. Concatenate Data, decode
// that JSON as the original DTO, then apply atomically. No fragment is a patch or
// message suffix by itself. Kind, placement, unknown values and explicit clears
// are preserved inside the assembled payload. Full-item replies may reuse the
// added-payload representation. BaseRev is present only for changes and appends.
type ThreadUpdatePart struct {
	ConversationID string              `json:"conversation_id"`
	Epoch          string              `json:"epoch"`
	Version        uint64              `json:"version"`
	ItemID         uint64              `json:"item_id"`
	BaseRev        *uint64             `json:"base_rev,omitempty"`
	Rev            uint64              `json:"rev"`
	Continuation   *ThreadContinuation `json:"continuation,omitempty"`
	Data           string              `json:"data"`
}

// EncodeThreadUpdate encodes one detached added/changed/append DTO value, without
// modifying inputs or performing I/O. Type and Payload in env are replaced.
// Every result is a complete Envelope fitting MaxThreadEnvelopeBytes. Ordinary
// shape is retained when it fits the reserved stamp budget; otherwise all parts
// carry assembly metadata. Any error returns nil, never a usable partial prefix.
//
// ID, TS, InReplyTo, EventID, HistoryEntryID and both booleans may be stamped later:
// maximum serialized widths are reserved even when absent. SessionID must be
// supplied before encoding and must not grow afterwards. Payload/Type must not
// change. The external relay routing wrapper is outside the plaintext cap.
// Inputs must not be mutated concurrently. Content stays inert and errors contain
// no input values. Consumers own semantic validation and assembly resource limits.
func EncodeThreadUpdate(env Envelope, update any) ([][]byte, error) {
	var part ThreadUpdatePart
	switch u := update.(type) {
	case ThreadItemAddedPayload:
		env.Type = TypeThreadItemAdded
		part = ThreadUpdatePart{ConversationID: u.ConversationID, Epoch: u.Epoch, Version: u.Version, ItemID: u.Item.ID, Rev: u.Item.Rev}
	case ThreadItemChangedPayload:
		env.Type = TypeThreadItemChanged
		part = ThreadUpdatePart{ConversationID: u.ConversationID, Epoch: u.Epoch, Version: u.Version, ItemID: u.ItemID, BaseRev: &u.BaseRev, Rev: u.Rev}
	case ThreadTextAppendPayload:
		env.Type = TypeThreadTextAppend
		part = ThreadUpdatePart{ConversationID: u.ConversationID, Epoch: u.Epoch, Version: u.Version, ItemID: u.ItemID, BaseRev: &u.BaseRev, Rev: u.Rev}
	default:
		return nil, ErrInvalidThreadUpdate
	}
	if !threadValidUTF8(reflect.ValueOf(update)) || !utf8.Valid(env.SessionID) {
		return nil, ErrInvalidThreadUpdate
	}
	logical, err := json.Marshal(update)
	if err != nil {
		return nil, ErrInvalidThreadUpdate
	}
	env.Payload = logical
	ordinary, err := json.Marshal(env)
	if err != nil {
		return nil, ErrInvalidThreadUpdate
	}
	budget := threadStampBudget(env)
	measured, err := json.Marshal(budget)
	if err != nil {
		return nil, ErrInvalidThreadUpdate
	}
	if len(measured) <= MaxThreadEnvelopeBytes {
		return [][]byte{ordinary}, nil
	}
	sum := sha256.Sum256([]byte(env.Type + "\x00" + string(logical)))
	part.Continuation = &ThreadContinuation{UpdateID: hex.EncodeToString(sum[:]), Index: math.MaxUint64, Offset: math.MaxUint64, TotalBytes: math.MaxUint64}
	budget.Payload, err = json.Marshal(part)
	if err != nil {
		return nil, ErrInvalidThreadUpdate
	}
	measured, err = json.Marshal(budget)
	if err != nil {
		return nil, ErrInvalidThreadUpdate
	}
	room := MaxThreadEnvelopeBytes - len(measured)
	if room <= 0 {
		return nil, ErrThreadUpdateMetadataTooLarge
	}
	var result [][]byte
	for offset := 0; offset < len(logical); {
		count := threadFragmentLength(logical[offset:], room)
		if count == 0 {
			return nil, ErrThreadUpdateMetadataTooLarge
		}
		part.Data = string(logical[offset : offset+count])
		part.Continuation.Index = uint64(len(result))
		part.Continuation.Offset = uint64(offset)
		part.Continuation.TotalBytes = uint64(len(logical))
		part.Continuation.Final = offset+count == len(logical)
		env.Payload, err = json.Marshal(part)
		if err != nil {
			return nil, ErrInvalidThreadUpdate
		}
		encoded, err := json.Marshal(env)
		if err != nil {
			return nil, ErrInvalidThreadUpdate
		}
		result = append(result, encoded)
		offset += count
	}
	return result, nil
}

func threadStampBudget(env Envelope) Envelope {
	n := uint64(math.MaxUint64)
	env.ID = n
	env.InReplyTo = &n
	env.EventID = &n
	env.HistoryEntryID = &n
	env.TS = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -86340))
	env.SessionStateCleared = true
	env.PayloadEncrypted = true
	return env
}

// JSON strings cost at most six serialized bytes per input byte. Search actual
// escaped cost so plain text can use the remaining frame budget too.
func threadFragmentLength(data []byte, room int) int {
	lo, hi := 0, min(len(data), room)
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		end := mid
		for end > 0 && end < len(data) && !utf8.RuneStart(data[end]) {
			end--
		}
		encoded, _ := json.Marshal(string(data[:end])) // valid UTF-8 string cannot fail
		if len(encoded)-2 <= room {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && lo < len(data) && !utf8.RuneStart(data[lo]) {
		lo--
	}
	return lo
}

// Reject invalid strings before encoding/json can silently replace their bytes.
func threadValidUTF8(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !threadValidUTF8(v.Field(i)) {
				return false
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if !threadValidUTF8(iter.Key()) || !threadValidUTF8(iter.Value()) {
				return false
			}
		}
	case reflect.Slice:
		return utf8.Valid(v.Bytes()) // all DTO slices are inert json.RawMessage
	}
	return true
}
