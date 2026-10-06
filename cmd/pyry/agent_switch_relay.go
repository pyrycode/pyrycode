package main

import (
	"context"
	"errors"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// relayAgentSwitcher projects daemon errors into the relay's closed vocabulary.
// No wrapped error or request value reaches diagnostics or a completion reply.
type relayAgentSwitcher struct{ switcher conversationAgentSwitcher }

func (a relayAgentSwitcher) SwitchAgent(ctx context.Context, p protocol.SwitchAgentPayload) relay.AgentSwitchOutcome {
	if !conversations.ValidID(p.ConversationID) {
		return relay.AgentSwitchOutcome{State: relay.AgentSwitchRefused, Failure: relay.AgentSwitchInvalidRequest}
	}
	var model *string
	if p.Model != "" {
		model = &p.Model
	}
	id, err := a.switcher.Switch(ctx, p.ConversationID, p.Agent, model, p.Effort)
	return agentSwitchOutcome(id, err)
}

func agentSwitchOutcome(id sessions.SessionID, err error) relay.AgentSwitchOutcome {
	if id != "" {
		return relay.AgentSwitchOutcome{State: relay.AgentSwitchCommitted}
	}
	// Post-wrap failures may join cleanup sentinels that look like admission
	// failures. Their static phase marker takes precedence over that classification.
	if errors.Is(err, ErrAgentSwitchUnavailable) || errors.Is(err, ErrAgentSwitchMintFailed) {
		return relay.AgentSwitchOutcome{}
	}
	failure := relay.AgentSwitchOtherFailure
	switch {
	case errors.Is(err, conversations.ErrConversationNotFound), errors.Is(err, sessions.ErrSessionNotFound):
		failure = relay.AgentSwitchConversationNotFound
	case errors.Is(err, ErrAgentSwitchSameAgent):
		failure = relay.AgentSwitchInvalidRequest
	case errors.Is(err, relay.ErrModelNotOffered):
		failure = relay.AgentSwitchModelNotOffered
	case errors.Is(err, relay.ErrEffortNotOffered):
		failure = relay.AgentSwitchEffortNotOffered
	case errors.Is(err, relay.ErrModelVocabularyUnavailable):
		failure = relay.AgentSwitchVocabularyUnavailable
	case errors.Is(err, ErrAgentSwitchInProgress):
		failure = relay.AgentSwitchBusy
	case errors.Is(err, handlers.ErrSpawnDirRejected):
		failure = relay.AgentSwitchWorkspaceRejected
	default:
		return relay.AgentSwitchOutcome{}
	}
	return relay.AgentSwitchOutcome{State: relay.AgentSwitchRefused, Failure: failure}
}

func announceSwitchedConversation(reg *conversations.Registry, id string, announce func(protocol.ConversationUpdatedPayload)) {
	row, ok := reg.Get(conversations.ConversationID(id))
	if !ok {
		return
	}
	var label *string
	if stored, ok := reg.WorkspaceLabel(row.Cwd); ok {
		label = &stored
	}
	announce(protocol.ConversationUpdatedPayload{WorkspaceLabel: label, ID: string(row.ID), IsPromoted: row.IsPromoted, IsArchived: row.IsArchived, IsMuted: row.IsMuted, ReadUpTo: row.ReadUpTo, Name: row.Name, Cwd: row.Cwd, LastUsedAt: row.LastUsedAt})
}
