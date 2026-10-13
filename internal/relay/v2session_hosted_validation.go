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
	id           uint64
	typ          string
	cursor       string
	cancel       protocol.AppCancelPayload
	asset        protocol.AppAssetRequestPayload
	api          protocol.AppAPIRequestPayload
	bodySize     int
	limit        bool
	credit       protocol.AppResponseCreditPayload
	creditTarget bool
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
	case protocol.TypeListApps, protocol.TypeApps, protocol.TypeAppUpdated, protocol.TypeAppRemoved, protocol.TypeAppCancel, protocol.TypeAppCancelled, protocol.TypeAppAssetRequest, protocol.TypeAppAPIRequest, protocol.TypeAppResponse, protocol.TypeAppResponseChunk, protocol.TypeAppResponseCredit:
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
	if payload, ok := obj["payload"].(map[string]any); ok && req.typ == protocol.TypeAppResponseCredit {
		req.credit.RequestID, req.creditTarget = hostedUint(payload["request_id"])
	}
	req.id, correlatable = hostedUint(obj["id"])
	correlatable = correlatable && !ambiguous && err == nil
	_, trailing := d.Token()
	if err != nil || trailing != io.EOF || invalid || !utf8.Valid(raw) || len(raw) > protocol.MaxThreadEnvelopeBytes || !correlatable {
		return
	}
	ts, ok := obj["ts"].(string)
	if !ok || !hostedTimestamp(ts) {
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
	case protocol.TypeAppAssetRequest, protocol.TypeAppAPIRequest:
		app, ok := payload["app_id"].(string)
		if !ok {
			return
		}
		if _, e := identity.ParseServerID(app); e != nil {
			return
		}
		release, ok := payload["release_version"].(string)
		version, canonical := protocol.ParseVersion(release)
		if !ok || !canonical || version.Major > 2147483647 || version.Minor > 2147483647 || version.Patch > 2147483647 {
			return
		}
		path, ok := payload["path"].(string)
		if !ok || !hostedASCII(path, 2048) || !strings.HasPrefix(path, "/") {
			return
		}
		req.asset = protocol.AppAssetRequestPayload{AppID: app, ReleaseVersion: release, Path: path}
		if v, exists := payload["query"]; exists {
			query, ok := v.(string)
			if !ok || !hostedASCII(query, 1024) || strings.HasPrefix(query, "?") {
				return
			}
			req.asset.Query = query
		}
		if req.typ == protocol.TypeAppAssetRequest {
			navigation, ok := payload["navigation"].(bool)
			if !ok {
				return
			}
			req.asset.Navigation = navigation
		} else {
			method, ok := payload["method"].(string)
			if !ok {
				return
			}
			body, ok := payload["body"].(string)
			if !ok {
				return
			}
			headers, ok := payload["headers"].(map[string]any)
			if !ok {
				return
			}
			req.api = protocol.AppAPIRequestPayload{AppID: app, ReleaseVersion: release, Path: path, Query: req.asset.Query, Method: method, Body: body, Headers: make(map[string]string, len(headers))}
			for key, value := range headers {
				text, ok := value.(string)
				if !ok {
					return
				}
				req.api.Headers[key] = text
			}
			req.bodySize, ok = hostedBodySize(body)
			if !ok {
				return
			}
			req.limit = req.bodySize > 32768
		}
	case protocol.TypeAppResponseCredit:
		app, ok := payload["app_id"].(string)
		if !ok {
			return
		}
		if _, e := identity.ParseServerID(app); e != nil {
			return
		}
		index, ok := hostedUint(payload["next_index"])
		if !ok || index > 255 || !req.creditTarget {
			return
		}
		req.credit.AppID, req.credit.NextIndex = app, index
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

// hostedBodySize validates canonical padded base64 without allocating decoded bytes.
func hostedBodySize(body string) (int, bool) {
	if len(body)%4 != 0 {
		return 0, false
	}
	data := strings.TrimRight(body, "=")
	padding := len(body) - len(data)
	if padding > 2 || (padding > 0 && len(data) == 0) {
		return 0, false
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := 0
	for i := 0; i < len(data); i++ {
		last = strings.IndexByte(alphabet, data[i])
		if last < 0 {
			return 0, false
		}
	}
	if (padding == 1 && last&3 != 0) || (padding == 2 && last&15 != 0) {
		return 0, false
	}
	return len(body)/4*3 - padding, true
}

// hostedTimestamp enforces RFC3339 UTC syntax before time.Parse checks dates.
// Go's parser also accepts a one-digit hour and comma fractional separator.
func hostedTimestamp(ts string) bool {
	if len(ts) < 20 || ts[len(ts)-1] != 'Z' {
		return false
	}
	for i := 0; i < 19; i++ {
		var separator byte
		switch i {
		case 4, 7:
			separator = '-'
		case 10:
			separator = 'T'
		case 13, 16:
			separator = ':'
		}
		if separator != 0 {
			if ts[i] != separator {
				return false
			}
		} else if ts[i] < '0' || ts[i] > '9' {
			return false
		}
	}
	if len(ts) == 20 {
		return true
	}
	if len(ts) < 22 || ts[19] != '.' {
		return false
	}
	for i := 20; i < len(ts)-1; i++ {
		if ts[i] < '0' || ts[i] > '9' {
			return false
		}
	}
	return true
}

func hostedASCII(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, b := range []byte(s) {
		if b > 127 {
			return false
		}
	}
	return true
}
