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
var atx = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?)|[ \t]*)$`)
var closingHashes = regexp.MustCompile(`[ \t]+#+[ \t]*$`)
var setext = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)

func parse(name string, data []byte) (int, string, []heading) {
	lines := strings.SplitAfter(string(data), "\n")
	offset, first, ticket, identity := 0, 0, 0, ""
	if m := filenameTicket.FindStringSubmatch(name); m != nil {
		var err error
		ticket, err = strconv.Atoi(m[1])
		if err != nil || ticket <= 0 {
			identity = "malformed filename ticket"
		}
	}
	text := func(i int) string { return strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r") }
	if strings.TrimPrefix(text(0), "\ufeff") == "---" {
		found, closed := false, false
		for i := 1; i < len(lines); i++ {
			if text(i) == "---" || text(i) == "..." {
				first = i + 1
				closed = true
				break
			}
			if strings.HasPrefix(text(i), "ticket:") {
				n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(text(i), "ticket:")))
				if found || err != nil || n <= 0 || !decimalTicket.MatchString(strings.TrimSpace(strings.TrimPrefix(text(i), "ticket:"))) {
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
	fenceChar, fenceSize := byte(0), 0
	paragraph := -1
	offsets := make([]int, len(lines))
	for i, line := range lines {
		offsets[i] = offset
		offset += len(line)
	}
	for i := first; i < len(lines); i++ {
		line := text(i)
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if indent <= 3 && len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			width := 0
			for width < len(trimmed) && trimmed[width] == trimmed[0] {
				width++
			}
			if fenceChar != 0 {
				if trimmed[0] == fenceChar && width >= fenceSize && strings.TrimSpace(trimmed[width:]) == "" {
					fenceChar = 0
				}
				paragraph = -1
				continue
			}
			if width >= 3 && (trimmed[0] == '~' || !strings.Contains(trimmed[width:], "`")) {
				fenceChar, fenceSize = trimmed[0], width
				paragraph = -1
				continue
			}
		}
		if fenceChar != 0 {
			continue
		}
		h := heading{start: offsets[i], body: offsets[i] + len(lines[i]), ordinal: len(headings) + 1}
		if m := atx.FindStringSubmatch(line); m != nil {
			h.depth = len(m[1])
			h.title = strings.TrimSpace(closingHashes.ReplaceAllString(m[2], ""))
		} else if m := setext.FindStringSubmatch(line); m != nil && paragraph >= 0 {
			h.depth = 2
			if m[1][0] == '=' {
				h.depth = 1
			}
			h.start = offsets[paragraph]
			h.title = strings.TrimSpace(strings.Join(lines[paragraph:i], ""))
		}
		if h.depth > 0 {
			headings = append(headings, h)
			paragraph = -1
		} else if strings.TrimSpace(line) == "" || indent >= 4 || strings.HasPrefix(trimmed, "\t") {
			paragraph = -1
		} else if paragraph < 0 {
			paragraph = i
		}
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
func trivial(body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(string(body))) {
	case "", "none", "none.", "no open questions.", "no outstanding questions.":
		return true
	}
	return false
}
