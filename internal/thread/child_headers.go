package thread

import "unicode"

// childHeader holds prepared values, without pointers a caller could mutate.
type childHeader struct {
	headline, errorSummary string
	errorKnown             bool
	childCount             uint64
	subtreeVersion         uint64
	countKnown, freshKnown bool
}

func prepareChildHeader(i Item) childHeader {
	h := childHeader{headline: childHeadline(i.Summary, i.Kind)}
	switch i.Status {
	case "failed", "denied":
		h.errorKnown, h.errorSummary = true, i.Status
	case "done", "completed", "finished":
		h.errorKnown = !i.Active
	}
	return h
}

func childHeadline(summary, kind string) string {
	if text := childHeaderText(summary); text != "" {
		return text
	}
	switch kind {
	case "tool_call":
		return "Tool call"
	case "agent":
		return "Agent"
	default:
		return "Thread item"
	}
}

// Stop after enough normalized runes to decide truncation; source text can be
// large. Whitespace and controls are separators, including at string boundaries.
func childHeaderText(s string) string {
	runes := make([]rune, 0, 201)
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = len(runes) != 0
			continue
		}
		if space {
			runes = append(runes, ' ')
			space = false
		}
		runes = append(runes, r)
		if len(runes) > 200 {
			return string(runes[:199]) + "…"
		}
	}
	return string(runes)
}

func (q *queryIndex) refreshHeader(row queryRow, version uint64) {
	q.membership(row, true)
	count := uint64(q.childRankWork(row.item.ID, ^uint64(0), ^uint64(0), true, true) - q.childRankWork(row.item.ID, 0, 0, false, true))
	row.header.childCount, row.header.countKnown = count, count < 1<<53
	row.header.subtreeVersion, row.header.freshKnown = version, version < 1<<53
	q.membership(row, false)
}

func (h childHeader) apply(s *ChildSummary) {
	s.Headline = h.headline
	if h.errorKnown {
		v := h.errorSummary
		s.ErrorSummary = &v
	}
	if h.countKnown {
		count, exists := h.childCount, h.childCount > 0
		s.PublicChildCount, s.HasPublicChildren = &count, &exists
	}
	if h.freshKnown {
		v := h.subtreeVersion
		s.SubtreeVersion = &v
	}
}
