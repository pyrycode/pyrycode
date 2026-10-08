package main

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	referenceRe = regexp.MustCompile(`^((?:cmd|internal)/(?:[A-Za-z0-9_.-]+/)*(?:[A-Za-z0-9_.-]*\.go)?)(?::[0-9]+(?:-[0-9]+)?)?(?:#.*)?$`)
	tableRowRe  = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$`)
)

func references(body string) []string {
	seen := make(map[string]bool)
	body = separateLinks(body)
	// Keep path punctuation inside tokens: splitting on slash, brackets, braces
	// or parentheses would turn rejected paths into qualifying interior pieces.
	for _, word := range referenceWords(body) {
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

func referenceWords(body string) []string {
	runes := []rune(body)
	var words []string
	start := 0
	for i, split := range referenceBoundaries(runes) {
		if split {
			if i > start {
				words = append(words, string(runes[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(runes) {
		words = append(words, string(runes[start:]))
	}
	return words
}

// referenceBoundaries keeps pipes inside code spans and path components intact.
// Only pipes outside those contexts in table rows separate cells. Commas between
// complete code spans separate citations, but commas inside paths stay intact.
func referenceBoundaries(runes []rune) []bool {
	boundaries := make([]bool, len(runes))
	tables := tableLines(string(runes))
	var closing []rune
	start, ticks, line := 0, 0, 0
	afterCode := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '`' {
			end := i + 1
			for end < len(runes) && runes[end] == '`' {
				end++
			}
			if ticks == 0 && strings.Trim(string(runes[start:i]), "[(<\"'!*_") == "" {
				ticks = end - i
			} else if ticks == end-i {
				ticks = 0
				afterCode = true
			}
			i = end - 1
			continue
		}
		split := unicode.IsSpace(r) || (r == '|' && tables[line] && ticks == 0 && len(closing) == 0 && !markdownEscaped(runes, i))
		if (r == ',' || r == ';') && afterCode && i+1 < len(runes) && runes[i+1] == '`' {
			split = true
		}
		if split {
			boundaries[i] = true
			start = i + 1
		}
		if r == '\n' {
			line++
		}
		afterCode = false
		if ticks == 0 && strings.ContainsRune("[]{}()<>", r) && !markdownEscaped(runes, i) {
			switch r {
			case '[':
				closing = append(closing, ']')
			case '{':
				closing = append(closing, '}')
			case '(':
				closing = append(closing, ')')
			case '<':
				closing = append(closing, '>')
			default:
				if len(closing) > 0 && r == closing[len(closing)-1] {
					closing = closing[:len(closing)-1]
				}
			}
		}
	}
	return boundaries
}

// tableLines recognizes outer-pipe rows and tables introduced by a Markdown
// delimiter row, including tables with no outer pipes.
func tableLines(body string) []bool {
	lines := strings.Split(body, "\n")
	tables := make([]bool, len(lines))
	inTable := false
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if tableRowRe.MatchString(line) {
			inTable = true
			if i > 0 {
				tables[i-1] = true
			}
		}
		if !strings.Contains(line, "|") {
			inTable = false
		}
		tables[i] = inTable || strings.HasPrefix(line, "|") || strings.HasSuffix(line, "|")
	}
	return tables
}

// separateLinks separates labels from destinations without splitting bracketed
// components inside a larger path. It also handles labels containing spaces.
func separateLinks(body string) string {
	runes := []rune(body)
	boundaries := referenceBoundaries(runes)
	var out strings.Builder
	start := 0
	for i := 0; i < len(runes); i++ {
		if runes[i] != '[' || markdownEscaped(runes, i) {
			continue
		}
		prefix := i
		for prefix > 0 && !boundaries[prefix-1] {
			prefix--
		}
		if strings.Trim(string(runes[prefix:i]), "([!*_`\"'") != "" {
			continue
		}
		labelEnd := matchingDelimiter(runes, i, '[', ']')
		if labelEnd < 0 || labelEnd+1 >= len(runes) || runes[labelEnd+1] != '(' {
			continue
		}
		linkEnd := matchingDelimiter(runes, labelEnd+1, '(', ')')
		if linkEnd < 0 {
			continue
		}
		out.WriteString(string(runes[start : labelEnd+1]))
		out.WriteByte(' ')
		start = labelEnd + 1
		i = linkEnd
	}
	out.WriteString(string(runes[start:]))
	return out.String()
}

func matchingDelimiter(body []rune, start int, open, close rune) int {
	depth := 0
	for i := start; i < len(body); i++ {
		if (body[i] == open || body[i] == close) && markdownEscaped(body, i) {
			continue
		}
		switch body[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// An odd run of backslashes escapes Markdown punctuation; paired backslashes
// represent literal backslashes and leave the following delimiter structural.
func markdownEscaped(body []rune, index int) bool {
	backslashes := 0
	for i := index - 1; i >= 0 && body[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 != 0
}
