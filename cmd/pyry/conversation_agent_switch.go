package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

var (
	ErrAgentSwitchSameAgent            = errors.New("conversation already uses target agent")
	ErrAgentSwitchInProgress           = errors.New("conversation reset already in progress")
	ErrAgentSwitchUnavailable          = errors.New("conversation agent switch unavailable")
	ErrAgentSwitchWorkspaceUnavailable = errors.New("conversation workspace unavailable")
	ErrAgentSwitchMintFailed           = errors.New("conversation agent switch mint failed")
	ErrAgentSwitchCleanupFailed        = errors.New("conversation agent switch cleanup failed")
)

// conversationAgentSwitcher is the daemon-side primitive consumed by the relay
// switch verb. A nonempty returned ID means the rebind committed, including when
// the old session's post-commit removal reports an error.
type conversationAgentSwitcher struct {
	pool          *sessions.Pool
	conversations *conversations.Registry
	registryPath  string
	reset         *conversationReset
	history       *history.Store
	resetting     *resettingEmitterV2
	saved         savedModelVocabulary
}

func (s conversationAgentSwitcher) Switch(ctx context.Context, convID, target string, model, effort *string) (sessions.SessionID, error) {
	if s.pool == nil || s.conversations == nil || s.reset == nil {
		return "", ErrAgentSwitchUnavailable
	}
	release, ok := s.reset.begin(convID)
	if !ok {
		return "", ErrAgentSwitchInProgress
	}
	defer release()

	conv, ok := s.conversations.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return "", conversations.ErrConversationNotFound
	}
	oldID := sessions.SessionID(conv.CurrentSessionID)
	oldAgent, err := s.pool.HarnessFor(oldID)
	if err != nil {
		return "", err
	}
	if target != protocol.AgentClaude && target != protocol.AgentCodex {
		return "", ErrAgentSwitchUnavailable
	}
	if target == oldAgent {
		return "", ErrAgentSwitchSameAgent
	}
	oldSettings, err := s.pool.SettingsFor(oldID)
	if errors.Is(err, sessions.ErrSessionNotFound) {
		oldSettings, err = s.pool.DormantSettingsFor(oldID)
		if err == nil {
			oldSettings.PermissionMode, err = s.pool.DormantStoredPostureFor(oldID)
		}
	}
	if err != nil {
		return "", err
	}

	start := s.pool.MintDefaults(target)
	if model != nil {
		start.Model = *model
	}
	list, have := agentModelVocabulary(s.pool, s.saved, target, "")
	if model != nil && *model != "" {
		if err := validateModelVocabulary(list, have, start.Model); err != nil {
			return "", err
		}
	}
	if effort != nil {
		start.Effort = *effort
		if err := validateEffortVocabulary(target, list, have, start.Model, start.Effort); err != nil {
			return "", err
		}
	} else if start.Model != "" && slices.Contains(effortLevelsFor(target, list, have, start.Model), oldSettings.Effort) {
		start.Effort = oldSettings.Effort
	} else {
		start.Effort = ""
	}
	start.YOLO = false
	start.PermissionMode = oldSettings.PermissionMode
	if oldSettings.YOLO || start.PermissionMode == sessions.PermissionModeBypass {
		start.PermissionMode = "default"
	}

	// Cwd is persisted data, so validate it again at this spawn boundary.
	spawnDir, err := resolveSpawnDir(conv.Cwd)
	if err != nil {
		if errors.Is(err, handlers.ErrSpawnDirRejected) {
			return "", handlers.ErrSpawnDirRejected
		}
		return "", ErrAgentSwitchWorkspaceUnavailable
	}
	s.resetting.wrappingUp(convID)
	// LIFO closes the reset sequence before begin's exclusion is released.
	defer s.resetting.done(convID)
	summary := ""
	if old, lookupErr := s.pool.Lookup(oldID); lookupErr == nil && old.Runner().State().ChildPID != 0 {
		text, ended, failed := s.reset.wrapUpText(convID)
		if ended && !failed {
			summary = text
		}
	}
	wrote := s.storeHandover(convID, summary)
	s.resetting.restarting(convID, wrote)

	newID, err := s.pool.MintWith(convID, spawnDir, target, start)
	if err != nil {
		// A runner construction error can contain the accepted workspace path.
		// Keep only static classifications at this relay-facing boundary.
		mintErr := error(ErrAgentSwitchMintFailed)
		if errors.Is(err, sessions.ErrPoolNotRunning) {
			mintErr = errors.Join(mintErr, sessions.ErrPoolNotRunning)
		}
		if newID != "" {
			if s.pool.Remove(ctx, newID, sessions.RemoveOptions{}) != nil {
				mintErr = errors.Join(mintErr, ErrAgentSwitchCleanupFailed)
			}
		}
		return "", mintErr
	}
	cleanup := func(cause error) (sessions.SessionID, error) {
		return "", errors.Join(cause, s.pool.Remove(ctx, newID, sessions.RemoveOptions{}))
	}

	committed, err := s.conversations.SwitchSession(conv.ID, string(oldID), string(newID), s.registryPath)
	if err != nil {
		if committed {
			return newID, fmt.Errorf("switch conversation: persist and rollback: %w", err)
		}
		return cleanup(fmt.Errorf("switch conversation: persist: %w", err))
	}

	removeErr := s.pool.Remove(ctx, oldID, sessions.RemoveOptions{JSONL: sessions.JSONLLeave})
	s.pool.PublishSwitchTransition(oldID, newID)
	if removeErr != nil {
		return newID, fmt.Errorf("switch conversation: remove old session: %w", removeErr)
	}
	return newID, nil
}
