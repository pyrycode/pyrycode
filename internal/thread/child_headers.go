package thread

import (
	"encoding/json"
	"unicode"
)

// childHeader holds prepared values, without pointers a caller could mutate.
type childHeader struct {
	headline, errorSummary string
	errorKnown             bool
	childCount             uint64
	subtreeVersion         uint64
	countKnown, freshKnown bool
	taskID                 string
	taskKnown              bool
}

// taskAssociation reads only the fold's computed scalar, never launch content.
// Creating recipes establish known absence; metadata without that authority
// stays unknown. Validated checkpoint recipes carry the same scalar authority.
func (h *childHeader) taskAssociation(i Item, r contentRecipe) {
	h.taskID, h.taskKnown = "", false
	if (i.Kind != "agent" && i.Kind != "tool_call") || r.creating == 0 || r.creating != i.ID {
		return
	}
	if field, ok := r.fields["task_id"]; ok {
		var id *string
		if !r.agent || field.entry != 0 || field.item != 0 || json.Unmarshal(field.value, &id) != nil || id == nil {
			return
		}
		h.taskID = *id
	}
	h.taskKnown = true
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

// refreshHeader removes the stored row, whose header keys rootTouches, because
// callers may pass the row with a replacement header already applied.
func (q *queryIndex) refreshHeader(row queryRow, version uint64) {
	if stored, ok := q.lookup(row.item.ID, true); ok {
		q.membership(stored, true)
	}
	count := uint64(q.childRankWork(row.item.ID, ^uint64(0), ^uint64(0), true, true) - q.childRankWork(row.item.ID, 0, 0, false, true))
	row.header.childCount, row.header.countKnown = count, count < 1<<53
	row.header.subtreeVersion, row.header.freshKnown = version, version < 1<<53
	q.membership(row, false)
}

func (h childHeader) apply(s *ChildSummary) {
	s.Headline = h.headline
	if h.taskKnown {
		v := h.taskID
		s.TaskID = &v
	}
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
