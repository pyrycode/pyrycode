package main

import (
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type startupTurnKey struct {
	source      history.SessionProvenance
	legacyScope int
	turnID      string
}

type startupMainTurn struct {
	key          startupTurnKey
	openingID    uint64
	open, ended  bool
	mainEvidence bool
	tools        map[string]string
	finished     map[string]bool
}

// reconcileStartupHistory runs under instance ownership before any producer.
// Only surviving nonempty raw logs get a divider; Page never creates a log.
func reconcileStartupHistory(store *history.Store, reg *conversations.Registry, logger *slog.Logger, at time.Time) {
	for _, conv := range reg.List() {
		if !conversations.ValidID(string(conv.ID)) {
			logger.Warn("history: startup read failed", "event", "startup_history.read_err", "reason", "invalid_id")
			continue
		}
		turns, nonempty, err := readStartupMainWork(store, conv.ID)
		var agents []*convTurnState
		var sends []history.Entry
		if err == nil && nonempty {
			agents, err = readStartupAgentWork(store, conv.ID)
		}
		if err == nil && nonempty {
			sends, err = readStartupSends(store, conv.ID)
		}
		if err != nil {
			logger.Warn("history: startup read failed", "event", "startup_history.read_err", "conversation_id", string(conv.ID), "reason", historyPageFailure(err))
			continue
		}
		if !nonempty {
			continue
		}
		closeStartupSends(store, logger, string(conv.ID), sends, at)
		closeStartupMainWork(store, logger, string(conv.ID), turns, at)
		closeStartupAgentWork(store, logger, agents, at)
		p := runtimeHistoryFact{ConversationID: string(conv.ID), Cause: "daemon_restart", OccurredAt: at}
		appendStartupHistoryFact(store, logger, historySessionDivider, p, history.SessionProvenance{Kind: "none"})
	}
}

// readStartupMainWork folds identities newest-first. Terminal facts win even
// over later output; delimiters bound matching only when provenance is absent.
func readStartupMainWork(store *history.Store, convID conversations.ConversationID) ([]*startupMainTurn, bool, error) {
	turns := make(map[startupTurnKey]*startupMainTurn)
	closedOpenings := make(map[uint64]bool)
	finishedOpenings := make(map[uint64]map[string]bool)
	childTurns := make(map[startupTurnKey]bool)
	var cursor string
	var scope int
	nonempty := false
	for {
		page, err := store.Page(convID, cursor, 128)
		if err != nil {
			return nil, false, err
		}
		nonempty = nonempty || len(page.Entries) != 0
		for _, entry := range page.Entries {
			// Decode only attribution and lifecycle fields, never retain content.
			var p struct {
				TurnID            string `json:"turn_id"`
				ToolUseID         string `json:"tool_use_id"`
				ToolCallID        string `json:"tool_call_id"`
				Name              string `json:"name"`
				Parent            string `json:"parent_tool_use_id"`
				Cause             string `json:"cause"`
				TurnOpenedEntryID uint64 `json:"turn_opened_entry_id"`
			}
			if json.Unmarshal(entry.Payload, &p) != nil {
				continue
			}
			if entry.Type == protocol.TypeSessionTransition || entry.Type == historySessionDivider {
				if p.Cause != "daemon_restart" {
					scope++
				}
				continue
			}
			if p.TurnID == "" {
				continue
			}
			key := startupTurnKey{legacyScope: scope, turnID: p.TurnID}
			if entry.Session != nil {
				key.source, key.legacyScope = *entry.Session, 0
			}
			if p.Parent != "" {
				childTurns[key] = true
				continue
			}
			var opens, ends, toolEnds bool
			switch entry.Type {
			case historyTurnOpened, protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult:
				opens = true
			case protocol.TypeTurnEnd, historyTurnInterrupted:
				ends = true
			case protocol.TypeToolDenied, historyToolInterrupted:
				toolEnds = true
			default:
				continue
			}
			toolID := p.ToolUseID
			if entry.Type == historyToolInterrupted {
				toolID = p.ToolCallID
			}
			// Explicit recovery references affect only the original opening,
			// never a reused identity in the scope where recovery was appended.
			if p.TurnOpenedEntryID != 0 && (entry.Type == historyTurnInterrupted || entry.Type == historyToolInterrupted) {
				if ends {
					closedOpenings[p.TurnOpenedEntryID] = true
				}
				if toolEnds && toolID != "" {
					if finishedOpenings[p.TurnOpenedEntryID] == nil {
						finishedOpenings[p.TurnOpenedEntryID] = make(map[string]bool)
					}
					finishedOpenings[p.TurnOpenedEntryID][toolID] = true
				}
				continue
			}
			st := turns[key]
			if st == nil {
				st = &startupMainTurn{key: key, tools: make(map[string]string), finished: make(map[string]bool)}
				turns[key] = st
			}
			if opens {
				st.open, st.openingID = true, entry.ID
				st.mainEvidence = true
			} else if entry.Type == protocol.TypeToolDenied {
				// Legacy denials can open a main turn, but carry no parent
				// field. Parent-attributed evidence disambiguates child denials.
				st.open, st.openingID = true, entry.ID
			}
			st.ended = st.ended || ends
			if (toolEnds || entry.Type == protocol.TypeToolResult) && toolID != "" {
				st.finished[toolID] = true
			}
			if entry.Type == protocol.TypeToolUse && toolID != "" && p.Name != "Agent" && p.Name != "Task" {
				st.tools[toolID] = p.Name
			}
		}
		if page.AtStart {
			break
		}
		cursor = page.Cursor
	}
	out := make([]*startupMainTurn, 0, len(turns))
	for _, st := range turns {
		if !st.open || st.ended || closedOpenings[st.openingID] || (!st.mainEvidence && childTurns[st.key]) {
			continue
		}
		for id := range st.tools {
			if st.finished[id] || finishedOpenings[st.openingID][id] {
				delete(st.tools, id)
			}
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b *startupMainTurn) int {
		if a.openingID < b.openingID {
			return -1
		}
		if a.openingID > b.openingID {
			return 1
		}
		return 0
	})
	return out, nonempty, nil
}

// closeStartupMainWork is the startup closure-before-divider point. Additional
// work families can close here before the caller appends its restart divider.
func closeStartupMainWork(store *history.Store, logger *slog.Logger, convID string, turns []*startupMainTurn, at time.Time) {
	for _, st := range turns {
		p := runtimeHistoryFact{ConversationID: convID, TurnID: st.key.turnID, TurnOpenedEntryID: st.openingID, Cause: "daemon_restart", OccurredAt: at}
		ids := make([]string, 0, len(st.tools))
		for id := range st.tools {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			p.ToolCallID, p.Tool = id, st.tools[id]
			appendStartupHistoryFact(store, logger, historyToolInterrupted, p, st.key.source)
		}
		p.ToolCallID, p.Tool = "", ""
		appendStartupHistoryFact(store, logger, historyTurnInterrupted, p, st.key.source)
	}
}

func appendStartupHistoryFact(store *history.Store, logger *slog.Logger, typ string, p runtimeHistoryFact, source history.SessionProvenance) {
	raw, err := json.Marshal(p)
	if err != nil {
		logger.Warn("history: startup fact marshal failed", "event", "startup_history.marshal_err")
		return
	}
	appendConversationHistory(store, logger, "startup_history.append_err", p.ConversationID, typ, raw, p.OccurredAt, source)
}
