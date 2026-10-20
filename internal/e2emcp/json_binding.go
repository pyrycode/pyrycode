package e2emcp

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// BindJSONString replaces exactly one source-validated scalar in a disposable
// binding copy. Every byte outside that scalar remains literal, including
// whitespace, key order, unknown fields and escape spelling. It is only a codec:
// the profile must separately authorize the exact typed path and both values.
// Array steps use canonical zero-based decimal indices; object steps use keys.
func BindJSONString(raw []byte, path []string, want, replacement string) ([]byte, error) {
	if !utf8.ValidString(want) || !utf8.ValidString(replacement) || !uniqueJSONDocument(raw, false) || len(path) == 0 || len(path) > 16 {
		return nil, protocolError()
	}
	for _, step := range path {
		if step == "" || len(step) > 128 {
			return nil, protocolError()
		}
	}
	result, err := bindJSONAt(raw, path, want, replacement)
	if err != nil || !uniqueJSONDocument(result, false) {
		return nil, protocolError()
	}
	return result, nil
}
func bindJSONAt(raw []byte, path []string, want, replacement string) ([]byte, error) {
	if len(path) == 0 {
		var actual string
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '"' || json.Unmarshal(trimmed, &actual) != nil || actual != want {
			return nil, protocolError()
		}
		if want == replacement {
			return append([]byte(nil), raw...), nil
		}
		encoded, err := json.Marshal(replacement)
		if err != nil {
			return nil, protocolError()
		}
		start := bytes.Index(raw, trimmed)
		out := append([]byte(nil), raw[:start]...)
		out = append(out, encoded...)
		out = append(out, raw[start+len(trimmed):]...)
		return out, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, protocolError()
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' && delimiter != '[' {
		return nil, protocolError()
	}
	arrayIndex := -1
	if delimiter == '[' {
		index, err := strconv.Atoi(path[0])
		if err != nil || index < 0 || strconv.Itoa(index) != path[0] {
			return nil, protocolError()
		}
		arrayIndex = index
	}
	index := 0
	for decoder.More() {
		key := ""
		if delimiter == '{' {
			token, err := decoder.Token()
			if err != nil {
				return nil, protocolError()
			}
			key, ok = token.(string)
			if !ok {
				return nil, protocolError()
			}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, protocolError()
		}
		end := int(decoder.InputOffset())
		start := end - len(value)
		if delimiter == '{' && key == path[0] || delimiter == '[' && index == arrayIndex {
			bound, err := bindJSONAt(value, path[1:], want, replacement)
			if err != nil {
				return nil, err
			}
			out := append([]byte(nil), raw[:start]...)
			out = append(out, bound...)
			out = append(out, raw[end:]...)
			return out, nil
		}
		index++
	}
	return nil, protocolError()
}
