package main

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	referenceRe = regexp.MustCompile(`^((?:cmd|internal)/(?:[A-Za-z0-9_.-]+/)*(?:[A-Za-z0-9_.-]*\.go)?)(?::[0-9]+(?:-[0-9]+)?)?(?:#.*)?$`)
	linkRe      = regexp.MustCompile(`\[[^\]]*\]\([^\)]*\)`)
)

func references(body string) []string {
	seen := make(map[string]bool)
	body = separateLinks(body)
	// Keep path punctuation inside tokens: splitting on slash, brackets, braces
	// or parentheses would turn rejected paths into qualifying interior pieces.
	words := strings.FieldsFunc(body, func(r rune) bool {
		return unicode.IsSpace(r) || r == '|'
	})
	for _, word := range words {
		part := strings.TrimLeft(word, "[(<`\"'")
		part = strings.TrimRight(part, "])>,;`\"'")
		if strings.HasSuffix(part, ".") {
			candidate := strings.TrimRight(strings.TrimSuffix(part, "."), "])>,;`\"'")
			// A sentence stop can follow a file citation. Removing dots after
			// a slash would turn trailing traversal/placeholders into a path.
			if !strings.HasSuffix(candidate, "/") && !strings.HasSuffix(candidate, ".") {
				part = candidate
			}
		}
		// Paired emphasis is Markdown; an unpaired star remains a glob.
		for len(part) > 1 && (part[0] == '*' || part[0] == '_') && part[len(part)-1] == part[0] {
			part = part[1 : len(part)-1]
		}
		match := referenceRe.FindStringSubmatch(part)
		if match == nil {
			continue
		}
		path := match[1]
		valid := true
		for _, component := range strings.Split(strings.TrimSuffix(path, "/"), "/") {
			if strings.Trim(component, ".") == "" {
				valid = false // traversal and ellipsis placeholders
				break
			}
		}
		if valid {
			seen[path] = true
		}
	}
	var paths []string
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// separateLinks separates labels from destinations without splitting bracketed
// components inside a larger path. It also handles labels containing spaces.
func separateLinks(body string) string {
	var out strings.Builder
	start := 0
	for _, span := range linkRe.FindAllStringIndex(body, -1) {
		prefix := span[0]
		for prefix > 0 && !unicode.IsSpace(rune(body[prefix-1])) && body[prefix-1] != '|' {
			prefix--
		}
		if strings.Trim(body[prefix:span[0]], "([!*_`\"'") != "" {
			continue
		}
		out.WriteString(body[start:span[0]])
		out.WriteString(strings.Replace(body[span[0]:span[1]], "](", "] (", 1))
		start = span[1]
	}
	out.WriteString(body[start:])
	return out.String()
}
