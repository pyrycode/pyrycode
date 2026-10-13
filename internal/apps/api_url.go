package apps

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func apiURL(path, query string) (string, bool) {
	if !strings.HasPrefix(path, "/api/") || !apiASCII(path, 2048) || strings.Contains(path, "//") || strings.ContainsAny(path, "\\?#") {
		return "", false
	}
	if query != "" && (!apiASCII(query, 1024) || strings.HasPrefix(query, "?") || strings.ContainsAny(query, "#\\")) {
		return "", false
	}
	if _, err := url.QueryUnescape(query); err != nil {
		return "", false
	}
	decoded, err := url.PathUnescape(path)
	if err != nil || !utf8.ValidString(decoded) {
		return "", false
	}
	if (&url.URL{Path: decoded, RawPath: path}).EscapedPath() != path {
		return "", false
	}
	for i := 0; i < len(path); i++ {
		if path[i] != '%' {
			continue
		}
		part, _ := url.PathUnescape(path[i : i+3])
		if part == "/" || part == "\\" || part == "%" {
			return "", false
		}
		i += 2
	}
	for _, r := range decoded {
		if unicode.IsControl(r) || r == '\\' {
			return "", false
		}
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return "", false
		}
	}
	return decoded, true
}
func apiASCII(s string, limit int) bool {
	if len(s) > limit {
		return false
	}
	for i := range len(s) {
		if s[i] <= 32 || s[i] >= 127 {
			return false
		}
	}
	return true
}
