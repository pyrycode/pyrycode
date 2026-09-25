package protocol

import "strconv"

// App names a hello's client_version may carry (docs/protocol-mobile.md
// § hello, v2-specific note). A daemon holds a minimum version per app name;
// a well-formed version of any other app name parses, and no minimum applies
// to it.
const (
	AppMobile  = "pyrycode-mobile"
	AppDesktop = "pyrycode-desktop"
)

// maxClientVersionBytes caps a client_version the parser will look at. It is
// the width internal/sessions admits a retained client_version into a session's
// system prompt at (its own maxClientVersionBytes), so every well-formed
// version stays admissible there instead of being silently dropped (#2576).
const maxClientVersionBytes = 32

// Version is a parsed MAJOR.MINOR.PATCH. Versions are ordered numerically,
// MAJOR first, and are only ever compared within one app name.
type Version struct {
	Major, Minor, Patch uint64
}

// Compare returns -1, 0 or +1 as v is below, equal to or above o. The first
// differing component decides.
func (v Version) Compare(o Version) int {
	for _, p := range [...][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		switch {
		case p[0] < p[1]:
			return -1
		case p[0] > p[1]:
			return 1
		}
	}
	return 0
}

// String renders v as the three-part wire form, e.g. "1.4.0" — the shape
// ErrorPayload.MinClientVersion carries.
func (v Version) String() string {
	return strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
}

// ParseClientVersion parses a hello's client_version as
// <app>/<MAJOR>.<MINOR>.<PATCH> (docs/protocol-mobile.md § hello, v2-specific
// note). ok is false for anything else, including every pre-#2576 free-text
// form such as "1.0".
//
// The input is remote-authored: the length is checked before anything is
// scanned, and the scan is a single pass over bytes with no regular expression.
func ParseClientVersion(s string) (app string, v Version, ok bool) {
	if len(s) > maxClientVersionBytes {
		return "", Version{}, false
	}
	slash := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			if slash >= 0 {
				return "", Version{}, false
			}
			slash = i
		}
	}
	if slash < 0 || !validAppName(s[:slash]) {
		return "", Version{}, false
	}
	v, ok = ParseVersion(s[slash+1:])
	if !ok {
		return "", Version{}, false
	}
	return s[:slash], v, true
}

// ParseVersion parses exactly three '.'-separated decimal integers: digits
// only, no leading zero except "0" itself, no prefix or suffix. A component
// that overflows uint64 is unparsable, as is anything over the client_version
// length cap.
func ParseVersion(s string) (Version, bool) {
	if len(s) > maxClientVersionBytes {
		return Version{}, false
	}
	var parts [3]uint64
	n := 0
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '.' {
			continue
		}
		if n == len(parts) {
			return Version{}, false
		}
		c, ok := parseComponent(s[start:i])
		if !ok {
			return Version{}, false
		}
		parts[n] = c
		n++
		start = i + 1
	}
	if n != len(parts) {
		return Version{}, false
	}
	return Version{Major: parts[0], Minor: parts[1], Patch: parts[2]}, true
}

// parseComponent parses one version component, rejecting an empty run, a
// non-digit byte, a leading zero and uint64 overflow.
func parseComponent(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		d := s[i]
		if d < '0' || d > '9' {
			return 0, false
		}
		if n > (1<<64-1-uint64(d-'0'))/10 {
			return 0, false
		}
		n = n*10 + uint64(d-'0')
	}
	return n, true
}

// validAppName reports whether s is lowercase ASCII letters, digits and '-',
// starting with a letter.
func validAppName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
