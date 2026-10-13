package apps

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validateAssetURL(escaped, query string) (string, error) {
	if !asciiURLText(escaped, 2048) || !strings.HasPrefix(escaped, "/") || strings.Contains(escaped, "//") || strings.ContainsAny(escaped, "\\?#") || !validEscapes(escaped, true) {
		return "", ErrAssetPath
	}
	if query != "" && (!asciiURLText(query, 1024) || strings.HasPrefix(query, "?") || strings.Contains(query, "#") || !validEscapes(query, false)) {
		return "", ErrAssetPath
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil || !utf8.ValidString(decoded) || strings.Contains(decoded, "\\") {
		return "", ErrAssetPath
	}
	for _, r := range decoded {
		if unicode.IsControl(r) {
			return "", ErrAssetPath
		}
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return "", ErrAssetPath
		}
	}
	for _, root := range []string{"/api", "/_pyry", "/service", "/dist/service", "/data", "/registry.json", "/source", "/staging", "/builds", "/backups", "/sql", "/node_modules"} {
		if decoded == root || strings.HasPrefix(decoded, root+"/") {
			return "", ErrAssetPath
		}
	}
	return decoded, nil
}
func asciiURLText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for i := range len(s) {
		if s[i] < 32 || s[i] >= 127 {
			return false
		}
	}
	return true
}
func validEscapes(s string, asset bool) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) {
			return false
		}
		hi, lo := urlHex(s[i+1]), urlHex(s[i+2])
		if hi < 0 || lo < 0 {
			return false
		}
		b := byte(hi<<4 | lo)
		if asset && (b == '/' || b == '\\' || b == '%') {
			return false
		}
		i += 2
	}
	return true
}
func urlHex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	default:
		return -1
	}
}
