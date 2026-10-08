package main

import (
	"regexp"
	"strconv"
	"strings"
)

type heading struct {
	start, body, end, depth, ordinal int
	title                            string
}

var decimalTicket = regexp.MustCompile(`^[0-9]+$`)
var filenameTicket = regexp.MustCompile(`^([0-9]+)(?:[-_]|\.md$)`)
var frontmatterTicket = regexp.MustCompile(`^[ \t]*ticket[ \t]*:(.*)$`)
var atx = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?)|[ \t]*)$`)
var closingHashes = regexp.MustCompile(`[ \t]+#+[ \t]*$`)
var setext = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
var thematicBreak = regexp.MustCompile(`^ {0,3}(?:\*(?:[ \t]*\*){2,}|-(?:[ \t]*-){2,}|_(?:[ \t]*_){2,})[ \t]*$`)
var containerStart = regexp.MustCompile(`^ {0,3}(?:>|(?:[-+*]|[0-9]{1,9}[.)])(?:[ \t]|$))`)
var containerText = regexp.MustCompile(`^ {0,3}(?:>[ \t]*|(?:[-+*]|[0-9]{1,9}[.)])[ \t]+)[^ \t]`)
var referenceStart = regexp.MustCompile(`^ {0,3}\[[^]]+\]:`)
var listMarker = regexp.MustCompile(`^(?:[-+*]|([0-9]{1,9})[.)])([ \t]*)`)

// A reference definition that is complete on one line: a label without
// brackets or escapes, a destination and an optional closed title.
var singleLineReference = regexp.MustCompile(`^ {0,3}\[([^\[\]\\]*)\]:[ \t]*(?:<[^<>\\]*>|[^ \t<>()\\\x00-\x1f]+)(?:[ \t]+(?:"[^"\\]*"|'[^'\\]*'|\([^()\\]*\)))?[ \t]*$`)

// HTML blocks end at their closing delimiter (types 1–5) or a blank line
// (types 6–7). Only type 7 cannot interrupt a paragraph.
var htmlBlocks = []struct{ start, end *regexp.Regexp }{
	{regexp.MustCompile(`(?i)^<(?:script|pre|style|textarea)(?:[ \t>]|$)`), regexp.MustCompile(`(?i)</(?:script|pre|style|textarea)>`)},
	{regexp.MustCompile(`^<!--`), regexp.MustCompile(`-->`)},
	{regexp.MustCompile(`^<\?`), regexp.MustCompile(`\?>`)},
	{regexp.MustCompile(`^<![A-Z]`), regexp.MustCompile(`>`)},
	{regexp.MustCompile(`^<!\[CDATA\[`), regexp.MustCompile(`\]\]>`)},
	{regexp.MustCompile(`(?i)^</?(?:address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h[1-6]|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|nav|noframes|ol|optgroup|option|p|param|search|section|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul)(?:[ \t>]|/>|$)`), regexp.MustCompile(`^[ \t]*$`)},
	{regexp.MustCompile("^<(?:(?:[A-Za-z][A-Za-z0-9-]*)(?:[ \\t]+[A-Za-z_:][A-Za-z0-9_.:-]*(?:[ \\t]*=[ \\t]*(?:[^ \\t\"'=<>`]+|'[^']*'|\"[^\"]*\"))?)*[ \\t]*/?|/[A-Za-z][A-Za-z0-9-]*[ \\t]*)>[ \\t]*$"), regexp.MustCompile(`^[ \t]*$`)},
}

// markdownBlank checks structural whitespace, which permits only spaces and
// tabs. Title and resolution matching separately trim surrounding whitespace.
func markdownBlank(line string) bool { return strings.Trim(line, " \t") == "" }

// markdownLines keeps original line-ending bytes for section offsets. CommonMark
// accepts LF, CRLF and lone CR; the final entry can be empty.
func markdownLines(data []byte) []string {
	text := string(data)
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '\n' && text[i] != '\r' {
			continue
		}
		if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
			i++
		}
		lines = append(lines, text[start:i+1])
		start = i + 1
	}
	return append(lines, text[start:])
}

// stripContainers removes leading block quote and list item markers. code
// reports container content that starts an indented code block.
func stripContainers(line string) (content string, stripped, code bool) {
	content = line
	for {
		rest := strings.TrimLeft(content, " \t")
		if strings.HasPrefix(rest, ">") {
			content, stripped = strings.TrimPrefix(rest[1:], " "), true
			if after := strings.TrimLeft(content, " "); !markdownBlank(after) && len(content)-len(after) >= 4 {
				return content, true, true
			}
			continue
		}
		m := listMarker.FindStringSubmatch(rest)
		if m == nil || (m[2] == "" && len(rest) > len(m[0])) {
			return content, stripped, false
		}
		content, stripped = rest[len(m[0]):], true
		if len(m[2]) >= 5 && !markdownBlank(content) {
			return content, true, true
		}
	}
}

// singleReference reports a complete one-line reference definition that no
// following line can extend with a title.
func singleReference(line, next string) bool {
	m := singleLineReference.FindStringSubmatch(line)
	if m == nil || strings.TrimSpace(m[1]) == "" || len(m[1]) > 999 {
		return false
	}
	next = strings.TrimLeft(next, " \t")
	return next == "" || !strings.ContainsAny(next[:1], "\"'(")
}

// parse returns the ticket identity, a reason when the document must stay
// unchanged, and the top-level headings. Constructs whose CommonMark block
// boundaries this parser does not model (fences, HTML or headings inside
// containers, lines less indented than an open fence, paragraphs that may be
// reference definitions) yield an "unsupported Markdown" reason instead of guessed
// section bounds.
func parse(name string, data []byte) (int, string, []heading) {
	lines := markdownLines(data)
	offset, first, ticket, identity := 0, 0, 0, ""
	if m := filenameTicket.FindStringSubmatch(name); m != nil {
		var err error
		ticket, err = strconv.Atoi(m[1])
		if err != nil || ticket <= 0 {
			identity = "malformed filename ticket"
		}
	}
	text := func(i int) string { return strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r") }
	nextText := func(i int) string {
		if i+1 < len(lines) {
			return text(i + 1)
		}
		return ""
	}
	if strings.TrimPrefix(text(0), "\ufeff") == "---" {
		found, closed := false, false
		for i := 1; i < len(lines); i++ {
			if text(i) == "---" || text(i) == "..." {
				first = i + 1
				closed = true
				break
			}
			if m := frontmatterTicket.FindStringSubmatch(text(i)); m != nil {
				value := strings.TrimSpace(m[1])
				n, err := strconv.Atoi(value)
				if found || err != nil || n <= 0 || !decimalTicket.MatchString(value) {
					identity = "malformed frontmatter ticket"
				} else if ticket != 0 && ticket != n {
					identity = "conflicting ticket identities"
				} else {
					ticket = n
				}
				found = true
			}
		}
		if !closed {
			return ticket, "unterminated frontmatter", nil
		}
	}
	if ticket == 0 && identity == "" {
		identity = "missing ticket identity"
	}
	var headings []heading
	var htmlEnd *regexp.Regexp
	fenceChar, fenceSize, fenceIndent := byte(0), 0, 0
	paragraph := -1
	lazyContainer := false
	// containerOpen holds from a container start until a column-0 line that
	// must close it. ambiguous marks the last column-0 line that may be a lazy
	// continuation of container content the parser does not track.
	containerOpen, previousIndented, previousBlank, ambiguous := false, false, true, -1
	unsupported := ""
	unsupportedAt := func(i int, construct string) {
		if unsupported == "" {
			unsupported = "unsupported Markdown: " + construct + " at line " + strconv.Itoa(i+1)
		}
	}
	offsets := make([]int, len(lines))
	for i, line := range lines {
		offsets[i] = offset
		offset += len(line)
	}
	for i := first; i < len(lines); i++ {
		line := text(i)
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		blank := markdownBlank(line)
		column0 := !blank && line[0] != ' ' && line[0] != '\t'
		wasIndented, wasBlank := previousIndented, previousBlank
		previousIndented, previousBlank = !blank && !column0, blank
		if htmlEnd == nil && fenceChar == 0 && containerStart.MatchString(line) {
			containerOpen = true
		} else if column0 && (wasBlank || atx.MatchString(line) || thematicBreak.MatchString(line) || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")) {
			containerOpen = false
		}
		if htmlEnd != nil {
			if htmlEnd.MatchString(line) {
				htmlEnd = nil
			}
			continue
		}
		// An indented fence may sit in a list item, which a less indented
		// line would close along with the fence.
		if fenceChar != 0 && indent < fenceIndent && !blank {
			unsupportedAt(i, "line less indented than its open fence")
		}
		if indent <= 3 && len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			width := 0
			for width < len(trimmed) && trimmed[width] == trimmed[0] {
				width++
			}
			if fenceChar != 0 {
				if trimmed[0] == fenceChar && width >= fenceSize && markdownBlank(trimmed[width:]) {
					fenceChar = 0
				}
				paragraph = -1
				continue
			}
			if width >= 3 && (trimmed[0] == '~' || !strings.Contains(trimmed[width:], "`")) {
				fenceChar, fenceSize, fenceIndent = trimmed[0], width, indent
				paragraph = -1
				lazyContainer = false
				continue
			}
		}
		if fenceChar != 0 {
			continue
		}
		content, contained, containedCode := stripContainers(line)
		inner := strings.TrimLeft(content, " \t")
		if containerOpen && column0 && wasIndented {
			ambiguous = i
			if htmlBlocks[6].start.MatchString(line) {
				unsupportedAt(i, "HTML that may continue container content")
			}
		}
		if containerOpen && (indent >= 4 || strings.HasPrefix(trimmed, "\t")) && (strings.HasPrefix(inner, "```") || strings.HasPrefix(inner, "~~~")) {
			unsupportedAt(i, "fence indented under a container")
		}
		if contained && strings.Contains(line[:len(line)-len(inner)], "\t") {
			unsupportedAt(i, "tab in container indentation")
		}
		if contained && (strings.HasPrefix(inner, "```") || strings.HasPrefix(inner, "~~~")) {
			unsupportedAt(i, "fence inside a container")
		}
		if (contained || !column0) && atx.MatchString(inner) {
			unsupportedAt(i, "indented or contained heading")
		}
		if contained && htmlStart(inner) {
			unsupportedAt(i, "HTML block inside a container")
		}
		if contained && referenceStart.MatchString(inner) {
			unsupportedAt(i, "reference definition inside a container")
		}
		if paragraph >= 0 && !lazyContainer && strings.HasPrefix(strings.TrimLeft(lines[paragraph], " "), "[") && htmlBlocks[6].start.MatchString(trimmed) {
			unsupportedAt(i, "HTML after a paragraph that may be a reference definition")
		}
		if setext.MatchString(inner) && ((contained && strings.Contains(line[:len(line)-len(content)], ">")) || (!column0 && lazyContainer)) {
			unsupportedAt(i, "contained Setext underline")
		}
		if indent <= 3 {
			for kind, block := range htmlBlocks {
				if (kind < 6 || (paragraph < 0 && !lazyContainer)) && block.start.MatchString(trimmed) {
					if indent > 0 {
						unsupportedAt(i, "indented HTML block")
					}
					htmlEnd = block.end
					paragraph = -1
					lazyContainer = false
					break
				}
			}
			if htmlEnd != nil {
				if htmlEnd.MatchString(line) {
					htmlEnd = nil
				}
				continue
			}
		}
		h := heading{start: offsets[i], body: offsets[i] + len(lines[i]), ordinal: len(headings) + 1}
		if m := atx.FindStringSubmatch(line); m != nil {
			h.depth = len(m[1])
			h.title = strings.TrimSpace(closingHashes.ReplaceAllString(m[2], ""))
		} else if m := setext.FindStringSubmatch(line); m != nil && paragraph >= 0 && !lazyContainer {
			h.depth = 2
			if m[1][0] == '=' {
				h.depth = 1
			}
			h.start = offsets[paragraph]
			h.title = strings.TrimSpace(strings.Join(lines[paragraph:i], ""))
			if opening := lines[paragraph]; opening[0] == ' ' || opening[0] == '\t' || opening[0] == '[' || ambiguous >= paragraph {
				unsupportedAt(paragraph, "Setext paragraph that is indented or may hold a reference definition")
			}
		}
		if h.depth > 0 {
			headings = append(headings, h)
			paragraph = -1
			lazyContainer = false
		} else if blank || thematicBreak.MatchString(line) {
			paragraph = -1
			lazyContainer = false
		} else if containerStart.MatchString(line) && !(paragraph >= 0 && !lazyContainer && !interrupts(line)) {
			paragraph = -1
			if lazyContainer && (containedCode || htmlStart(inner)) {
				unsupportedAt(i, "container line that may continue a container paragraph")
			}
			lazyContainer = containerText.MatchString(line) && !containedCode && !thematicBreak.MatchString(inner) && !htmlStart(inner)
		} else if lazyContainer {
			paragraph = -1
		} else if paragraph >= 0 {
			// Indented lines, reference-like lines and list items that
			// cannot interrupt a paragraph continue it.
		} else if indent >= 4 || strings.HasPrefix(trimmed, "\t") {
			paragraph = -1
		} else if referenceStart.MatchString(line) && singleReference(line, nextText(i)) {
			paragraph = -1
		} else {
			paragraph = i
		}
	}
	if identity == "" {
		identity = unsupported
	}
	for i := range headings {
		headings[i].end = len(data)
		for j := i + 1; j < len(headings); j++ {
			if headings[j].depth <= headings[i].depth {
				headings[i].end = headings[j].start
				break
			}
		}
	}
	return ticket, identity, headings
}

// interrupts reports whether a list item or block quote start may interrupt
// a paragraph: block quotes always, list items only when nonempty and, for
// ordered lists, numbered 1.
func interrupts(line string) bool {
	rest := strings.TrimLeft(line, " ")
	if strings.HasPrefix(rest, ">") {
		return true
	}
	m := listMarker.FindStringSubmatch(rest)
	if m == nil || markdownBlank(rest[len(m[0]):]) {
		return false
	}
	return m[1] == "" || m[1] == "1"
}
func htmlStart(content string) bool {
	for _, block := range htmlBlocks {
		if block.start.MatchString(content) {
			return true
		}
	}
	return false
}
func trivial(body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(string(body))) {
	case "", "none", "none.", "no open questions.", "no outstanding questions.":
		return true
	}
	return false
}
