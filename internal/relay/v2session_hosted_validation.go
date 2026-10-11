package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const hostedMaxID = uint64(9007199254740991)

type hostedRequest struct {
	id     uint64
	typ    string
	cursor string
	cancel protocol.AppCancelPayload
}

// hostedJSON preserves exact keys and marks duplicates even inside additive fields.
func hostedJSON(d *json.Decoder, depth int, invalid *bool, ambiguous *bool) (any, error) {
	if depth > 128 {
		return nil, io.ErrUnexpectedEOF
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	if delim == '{' {
		obj := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			key := k.(string)
			v, e := hostedJSON(d, depth+1, invalid, ambiguous)
			if e != nil {
				return obj, e
			}
			if _, exists := obj[key]; exists {
				*invalid = true
				if depth == 0 && key == "id" {
					*ambiguous = true
				}
				if depth == 0 && key == "type" {
					if typ, ok := obj[key].(string); ok && hostedType(typ) {
						continue
					}
				}
			}
			obj[key] = v
		}
		_, err = d.Token()
		return obj, err
	}
	if delim == '[' {
		var values []any
		for d.More() {
			v, e := hostedJSON(d, depth+1, invalid, ambiguous)
			if e != nil {
				return nil, e
			}
			values = append(values, v)
		}
		_, err = d.Token()
		return values, err
	}
	return nil, io.ErrUnexpectedEOF
}
func hostedUint(v any) (uint64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	s := n.String()
	if strings.ContainsAny(s, ".-+eE") {
		return 0, false
	}
	u, e := strconv.ParseUint(s, 10, 64)
	return u, e == nil && u <= hostedMaxID
}
func hostedType(typ string) bool {
	switch typ {
	case protocol.TypeListApps, protocol.TypeApps, protocol.TypeAppUpdated, protocol.TypeAppRemoved, protocol.TypeAppCancel, protocol.TypeAppCancelled:
		return true
	}
	return false
}

// parseHostedRequest never decodes through a case-insensitive struct matcher.
func parseHostedRequest(raw []byte) (req hostedRequest, recognized, correlatable, valid bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	invalid, ambiguous := false, false
	value, err := hostedJSON(d, 0, &invalid, &ambiguous)
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	req.typ, _ = obj["type"].(string)
	recognized = hostedType(req.typ)
	if !recognized { // Case aliases can identify this boundary, but cannot authorize dispatch.
		for key, v := range obj {
			if strings.EqualFold(key, "type") {
				typ, _ := v.(string)
				if hostedType(typ) {
					recognized = true
				}
			}
		}
	}
	req.id, correlatable = hostedUint(obj["id"])
	correlatable = correlatable && !ambiguous && err == nil
	_, trailing := d.Token()
	if err != nil || trailing != io.EOF || invalid || !utf8.Valid(raw) || len(raw) > protocol.MaxThreadEnvelopeBytes || !correlatable {
		return
	}
	ts, ok := obj["ts"].(string)
	if !ok || !strings.HasSuffix(ts, "Z") {
		return
	}
	if _, e := time.Parse(time.RFC3339Nano, ts); e != nil {
		return
	}
	payload, ok := obj["payload"].(map[string]any)
	if !ok {
		return
	}
	for _, key := range []string{"conversation_id", "session_id", "event_id", "history_entry_id", "session_state_cleared", "payload_encrypted", "in_reply_to"} {
		if _, exists := obj[key]; exists {
			return
		}
	}
	switch req.typ {
	case protocol.TypeListApps:
		if v, exists := payload["cursor"]; exists {
			cursor, ok := v.(string)
			if !ok || len(cursor) == 0 || len(cursor) > 256 {
				return
			}
			for _, b := range []byte(cursor) {
				if b > 127 {
					return
				}
			}
			req.cursor = cursor
		}
	case protocol.TypeAppCancel:
		target, ok := hostedUint(payload["request_id"])
		if !ok {
			return
		}
		req.cancel.RequestID = target
		if v, exists := payload["app_id"]; exists {
			id, ok := v.(string)
			if !ok {
				return
			}
			if _, e := identity.ParseServerID(id); e != nil {
				return
			}
			req.cancel.AppID = id
		}
	default:
		return
	}
	valid = true
	return
}
