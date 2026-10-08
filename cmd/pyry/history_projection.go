package main

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// legacyHistoryType is the closed vocabulary of the existing history producers.
// Visibility metadata never grants a new fact access to legacy transports.
func legacyHistoryType(typ string) bool {
	switch typ {
	case protocol.TypeMessage, protocol.TypeAssistantDelta,
		protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied,
		protocol.TypeTurnEnd, protocol.TypeBanner, protocol.TypeBackgroundTaskStarted,
		protocol.TypeBackgroundTaskUpdated, protocol.TypeCompactionBoundary,
		protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback,
		protocol.TypeUnrecognizedMessage, protocol.TypeSessionTransition,
		protocol.TypeTurnState, protocol.TypeStall, protocol.TypeApiRetry,
		protocol.TypeCompacting, protocol.TypeToolProgress, protocol.TypeThinkingProgress,
		protocol.TypeBackgroundTaskRoster, protocol.TypeBackgroundTaskProgress,
		protocol.TypeRateLimited, protocol.TypeContextUsage, protocol.TypeModelAnnounced,
		protocol.TypeSessionFacts, protocol.TypeMCPStatus, protocol.TypeModelList,
		protocol.TypeSlashCommandList:
		return true
	default:
		return false
	}
}

// historyVisibilityMetadata classifies new entries without changing payloads or
// attributing a session. Old entries keep the store's absent-metadata behavior.
func historyVisibilityMetadata(typ string, raw json.RawMessage) history.Metadata {
	shown := historyEntryShown(typ, raw)
	return history.Metadata{Shown: &shown}
}

func historyEntryShown(typ string, raw json.RawMessage) bool {
	switch typ {
	case protocol.TypeTurnState, protocol.TypeStall, protocol.TypeApiRetry,
		protocol.TypeCompacting, protocol.TypeToolProgress, protocol.TypeThinkingProgress,
		protocol.TypeBackgroundTaskRoster, protocol.TypeBackgroundTaskProgress,
		protocol.TypeRateLimited, protocol.TypeContextUsage, protocol.TypeModelAnnounced,
		protocol.TypeSessionFacts, protocol.TypeMCPStatus, protocol.TypeModelList,
		protocol.TypeSlashCommandList:
		return false
	case protocol.TypeTurnEnd:
		var p protocol.TurnEndPayload
		if json.Unmarshal(raw, &p) != nil {
			return true
		}
		return !(p.StopReason == "end_turn" && !p.IsError &&
			(p.Outcome == "" || p.Outcome == "success") &&
			(p.TerminalReason == "" || p.TerminalReason == "completed") && p.ErrorCategory == "")
	case protocol.TypeBanner:
		var p protocol.BannerPayload
		if json.Unmarshal(raw, &p) != nil {
			return true
		}
		return p.Level != "info" || p.StopsTurn
	case protocol.TypeBackgroundTaskUpdated:
		var p protocol.BackgroundTaskUpdatedPayload
		if json.Unmarshal(raw, &p) != nil {
			return true
		}
		// Nonempty status is the existing terminal notification contract; its
		// vocabulary is open, so unknown terminal values still count as content.
		return p.Status != "" || p.Summary != ""
	case protocol.TypeSessionTransition:
		var p protocol.SessionTransitionPayload
		if json.Unmarshal(raw, &p) != nil {
			return true
		}
		return p.Reason != "idle_evict"
	default:
		return legacyHistoryType(typ)
	}
}
