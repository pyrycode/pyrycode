package main

import (
	"context"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const backgroundTaskStopTimeout = 30 * time.Second

type backgroundTaskChild interface {
	BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool)
	StopTask(context.Context, string) bool
}

type backgroundTaskStopper struct {
	reg  *conversations.Registry
	pool *sessions.Pool
}

func boundBackgroundTaskStopper(reg *conversations.Registry, pool *sessions.Pool) relay.BackgroundTaskStopper {
	return backgroundTaskStopper{reg: reg, pool: pool}
}

// StopBackgroundTask resolves a single stored binding, never the active cursor.
// Roster provenance and actuation belong to that same runner, even across a rebind.
// No child-authored or client-authored text reaches a log on this path.
func (s backgroundTaskStopper) StopBackgroundTask(ctx context.Context, conversationID, taskID string) relay.BackgroundTaskStopOutcome {
	if !conversations.ValidID(conversationID) || s.reg == nil || s.pool == nil {
		return relay.BackgroundTaskStopCannotActOnConversation
	}
	conv, ok := s.reg.Get(conversations.ConversationID(conversationID))
	if !ok || conv.CurrentSessionID == "" {
		return relay.BackgroundTaskStopCannotActOnConversation
	}
	id := sessions.SessionID(conv.CurrentSessionID)
	sess, err := s.pool.Lookup(id)
	if err != nil {
		return relay.BackgroundTaskStopCannotActOnConversation
	}
	harness, err := s.pool.HarnessFor(id)
	if err != nil || harness != sessions.HarnessClaude {
		return relay.BackgroundTaskStopRefused
	}
	runner := sess.Runner()
	child, ok := runner.(backgroundTaskChild)
	if !ok || runner.State().ChildPID <= 0 {
		return relay.BackgroundTaskStopRefused
	}
	roster, reported := child.BackgroundTaskRoster()
	if reported && taskID != "" {
		for _, row := range roster.Tasks {
			if row.TaskID != taskID {
				continue
			}
			if slices.Contains(row.TruncatedFields, "task_id") {
				return relay.BackgroundTaskStopRefused
			}
			bounded, cancel := context.WithTimeout(ctx, backgroundTaskStopTimeout)
			defer cancel()
			if child.StopTask(bounded, row.TaskID) && bounded.Err() == nil {
				return relay.BackgroundTaskStopAccepted
			}
			return relay.BackgroundTaskStopRefused
		}
	}
	return relay.BackgroundTaskStopRefused
}
