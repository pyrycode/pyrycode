// Package apps validates host-app manifests and persists independent registrations.
package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/identity"
)

var ErrInvalidManifest = errors.New("apps: invalid manifest")
var ErrForeignHost = errors.New("apps: foreign host")

// Manifest is an immutable, validated v1 manifest. Validation proves no readiness.
// Its zero value is invalid. JSON returns a detached canonical representation.
type Manifest struct{ canonical, serverID, appID, title, releaseVersion string }

func (m Manifest) ServerID() string       { return m.serverID }
func (m Manifest) AppID() string          { return m.appID }
func (m Manifest) Title() string          { return m.title }
func (m Manifest) ReleaseVersion() string { return m.releaseVersion }
func (m Manifest) JSON() []byte           { return []byte(m.canonical) }
func (m Manifest) MarshalJSON() ([]byte, error) {
	if m.canonical == "" {
		return nil, ErrInvalidManifest
	}
	return m.JSON(), nil
}
func (m *Manifest) UnmarshalJSON(data []byte) error {
	parsed, e := parseManifest(data)
	if e == nil {
		*m = parsed
	}
	return e
}

const fixedManifest = `{"contract_version":1,"runtime":{"node":">=24.15.0 <25.0.0","sqlite":"node:sqlite"},"frontend":{"root":"dist/web","entry":"index.html"},"service":{"entry":"dist/service/main.js","migrate":"dist/service/migrate.js","health_path":"/_pyry/health","migrations":"sql/migrations"},"build":{"package":"package.json","lockfile":"package-lock.json","install":["npm","ci"],"typecheck":["npm","run","typecheck"],"test":["npm","test"],"compile":["npm","run","build"]}}`

// ValidateManifest accepts only canonical identities belonging to serverID.
// It neither restamps identities nor inspects files or executes manifest commands.
func ValidateManifest(data []byte, serverID string) (Manifest, error) {
	if !validID(serverID) {
		return Manifest{}, ErrInvalidManifest
	}
	m, e := parseManifest(data)
	if e != nil {
		return Manifest{}, e
	}
	if m.serverID != serverID {
		return Manifest{}, ErrForeignHost
	}
	return m, nil
}
func parseManifest(data []byte) (Manifest, error) {
	if len(data) > 16384 {
		return Manifest{}, ErrInvalidManifest
	}
	value, e := strictJSON(data)
	if e != nil {
		return Manifest{}, ErrInvalidManifest
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return Manifest{}, ErrInvalidManifest
	}
	get := func(k string) string { s, _ := fields[k].(string); return s }
	m := Manifest{serverID: get("server_id"), appID: get("app_id"), title: get("title"), releaseVersion: get("release_version")}
	if !validID(m.serverID) || !validID(m.appID) || !validText(m.title, 128, true) || !validRelease(m.releaseVersion) {
		return Manifest{}, ErrInvalidManifest
	}
	if v, exists := fields["description"]; exists {
		s, ok := v.(string)
		if !ok || !validText(s, 512, false) {
			return Manifest{}, ErrInvalidManifest
		}
	}
	canonical, e := json.Marshal(fields)
	if e != nil {
		return Manifest{}, ErrInvalidManifest
	}
	m.canonical = string(canonical)
	for _, key := range []string{"server_id", "app_id", "title", "release_version", "description"} {
		delete(fields, key)
	}
	expected, e := strictJSON([]byte(fixedManifest))
	if e != nil || !reflect.DeepEqual(fields, expected) {
		return Manifest{}, ErrInvalidManifest
	}
	return m, nil
}
func validID(s string) bool { _, e := identity.ParseServerID(s); return e == nil }
func validText(s string, max int, trim bool) bool {
	if len(s) < 1 || len(s) > max || !utf8.ValidString(s) || (trim && strings.TrimSpace(s) != s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validRelease(s string) bool {
	if len(s) > 32 {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
		if _, e := strconv.ParseUint(p, 10, 31); e != nil {
			return false
		}
	}
	return true
}

// strictJSON preserves exact keys and integer spellings, rejects null and duplicate
// keys at any depth, and bounds recursion before decoding nested caller data.
func strictJSON(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, ErrInvalidManifest
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, e := jsonValue(d, 0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalidManifest
	}
	return v, nil
}
func jsonValue(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, ErrInvalidManifest
	}
	token, e := d.Token()
	if e != nil || token == nil {
		return nil, ErrInvalidManifest
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return nil, e
			}
			s, ok := key.(string)
			if !ok {
				return nil, ErrInvalidManifest
			}
			if _, exists := m[s]; exists {
				return nil, ErrInvalidManifest
			}
			v, e := jsonValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[s] = v
		}
		_, e = d.Token()
		return m, e
	case '[':
		values := []any{}
		for d.More() {
			v, e := jsonValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			values = append(values, v)
		}
		_, e = d.Token()
		return values, e
	default:
		return nil, ErrInvalidManifest
	}
}
