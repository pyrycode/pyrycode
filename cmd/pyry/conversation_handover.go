package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func (s conversationAgentSwitcher) storeHandover(convID, summary string) bool {
	if _, ok := sessions.FencedHandoffNote(summary); !ok {
		summary = s.reset.previousNote(convID)
		if _, ok := sessions.FencedHandoffNote(summary); !ok {
			summary = ""
		}
	}
	ctx, cancel := context.WithTimeout(s.reset.baseContext(), s.reset.bound())
	defer cancel()
	exchanges := switchRecentExchanges(ctx, s.history, convID)
	pointer := ""
	if s.history != nil {
		if dir, err := s.history.LogDir(conversations.ConversationID(convID)); err == nil {
			pointer = "\nConversation log: " + dir + ". Segment files are JSON Lines: a schema header followed by one JSON event per line.\n"
		}
	}
	combined := composeSwitchHandoff(summary, exchanges, pointer)
	if _, ok := sessions.FencedHandoffNote(combined); ok {
		return s.reset.storeNote(convID, combined)
	}
	return s.reset.storeNote(convID, boundedSwitchSummary(summary, sessions.MaxHandoffNoteBytes))
}

func boundedSwitchSummary(summary string, limit int) string {
	if len(summary) <= limit {
		return summary
	}
	for limit > 0 && !utf8.RuneStart(summary[limit]) {
		limit--
	}
	return summary[:limit]
}

func composeSwitchHandoff(summary string, exchanges []string, pointer string) string {
	const summaryHeading = "Summary:\n"
	const exchangesHeading = "\nRecent exchanges:\n"
	if len(pointer) > sessions.MaxHandoffNoteBytes-len(summaryHeading)-1 {
		pointer = ""
	}
	render := func() string {
		text := ""
		if summary != "" {
			text = summaryHeading + summary + "\n"
		}
		if len(exchanges) != 0 {
			text += exchangesHeading + strings.Join(exchanges, "")
		}
		return text + pointer
	}
	for len(exchanges) > 0 && len(render()) > sessions.MaxHandoffNoteBytes {
		exchanges = exchanges[1:]
	}
	if len(render()) > sessions.MaxHandoffNoteBytes {
		summary = boundedSwitchSummary(summary, sessions.MaxHandoffNoteBytes-len(summaryHeading)-1-len(pointer))
	}
	return render()
}

// switchRecentExchanges walks completed turn intervals backwards, then renders
// them chronologically. Incomplete tails and assistant-only wrap-up turns have
// no user message and cannot become exchanges. Any page failure drops the batch.
func switchRecentExchanges(ctx context.Context, store *history.Store, convID string) []string {
	if store == nil {
		return nil
	}
	type row struct{ role, text string }
	var rows []row
	var exchanges []string
	cursor := ""
	ended, hasUser, hasAssistant := false, false, false
	bytes := 0
	finish := func() {
		if !hasUser || !hasAssistant {
			return
		}
		if bytes > sessions.MaxHandoffNoteBytes {
			// An oversized exchange still occupies its chronological slot; composition
			// drops it whole. Do not retain unbounded historical text while paging.
			exchanges = append(exchanges, strings.Repeat("x", sessions.MaxHandoffNoteBytes+1))
			return
		}
		var text strings.Builder
		previousRole := ""
		for i := len(rows) - 1; i >= 0; i-- {
			item := rows[i]
			if item.role == "User" || item.role != previousRole {
				if previousRole != "" {
					text.WriteByte('\n')
				}
				text.WriteString(item.role + ":\n")
			}
			text.WriteString(item.text)
			previousRole = item.role
		}
		text.WriteByte('\n')
		exchanges = append(exchanges, text.String())
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		page, err := store.Page(conversations.ConversationID(convID), cursor, 128)
		if err != nil {
			return nil
		}
		for _, entry := range page.Entries {
			if entry.Type == protocol.TypeTurnEnd {
				if ended {
					finish()
				}
				if len(exchanges) == 3 {
					slices.Reverse(exchanges)
					return exchanges
				}
				ended, hasUser, hasAssistant, bytes = true, false, false, 0
				rows = nil
				continue
			}
			if !ended {
				continue
			}
			item := row{}
			switch entry.Type {
			case protocol.TypeMessage:
				var msg protocol.MessagePayload
				if json.Unmarshal(entry.Payload, &msg) == nil && msg.Role == "user" {
					item = row{"User", msg.Text}
					hasUser = true
				}
			case protocol.TypeAssistantDelta:
				var delta protocol.AssistantDeltaPayload
				if json.Unmarshal(entry.Payload, &delta) == nil && delta.ParentToolUseID == "" && delta.Text != "" {
					item = row{"Assistant", delta.Text}
					hasAssistant = true
				}
			}
			if item.role != "" {
				merge := item.role == "Assistant" && len(rows) > 0 && rows[len(rows)-1].role == "Assistant"
				bytes += len(item.text)
				if !merge {
					bytes += len(item.role) + 3
				}
				if bytes <= sessions.MaxHandoffNoteBytes {
					if merge {
						rows[len(rows)-1].text = item.text + rows[len(rows)-1].text
					} else {
						rows = append(rows, item)
					}
				} else {
					rows = nil
				}
			}
		}
		if page.AtStart {
			if ended {
				finish()
			}
			slices.Reverse(exchanges)
			return exchanges
		}
		cursor = page.Cursor
	}
}
