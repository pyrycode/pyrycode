package thread

import (
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

type boundaryFact struct {
	Cause               string    `json:"cause"`
	Reason              string    `json:"reason"`
	OccurredAt          time.Time `json:"occurred_at"`
	PreviousSessionID   string    `json:"previous_session_id"`
	NewSessionID        string    `json:"new_session_id"`
	PreviousAgent       string    `json:"previous_agent"`
	NextAgent           string    `json:"next_agent"`
	ResetHandoffOutcome *string   `json:"reset_handoff_outcome"`
	WorkspaceCwd        *string   `json:"workspace_cwd"`
}
type boundaryKey struct {
	at                     time.Time
	previous, next, reason string
}

func (f *Fold) boundary(e history.Entry, item *Item) bool {
	var p boundaryFact
	if !decode(e.Payload, &p, "occurred_at") || p.OccurredAt.IsZero() {
		return false
	}
	raw := e.Type == "session_divider"
	reason := p.Reason
	if raw {
		switch p.Cause {
		case "operator_reset", "claude_clear", "agent_switch":
			reason = "clear"
		case "idle_sleep", "capacity_eviction":
			reason = "idle_evict"
		case "recovery", "workspace_change", "daemon_restart":
			reason = ""
		default:
			return false
		}
	} else {
		switch reason {
		case "clear", "idle_evict", "recovered", "workspace_change":
		default:
			return false
		}
	}
	if (p.PreviousSessionID == "" && (!raw || p.Cause != "daemon_restart")) || (!raw && p.NewSessionID == "") {
		return false
	}
	key := boundaryKey{at: p.OccurredAt.UTC(), previous: p.PreviousSessionID, next: p.NewSessionID, reason: reason}
	if reason == "idle_evict" && raw {
		key.next = p.PreviousSessionID
	}
	if !raw && len(f.pending[key]) > 0 {
		ids := f.pending[key]
		id := ids[0]
		if len(ids) == 1 {
			delete(f.pending, key)
		} else {
			f.pending[key] = ids[1:]
		}
		// A late companion may supplement only this boundary's successor. It
		// cannot change earlier items or move attribution past a newer boundary.
		if id == f.lastBoundary && reason == "clear" && f.successor.Kind == "" && e.Session != nil && e.Session.SessionID == f.successor.SessionID {
			f.successor.Kind = recordedAgent(e.Session.Kind)
		}
		return false
	}
	if raw && reason != "" {
		f.pending[key] = append(f.pending[key], e.ID)
	}
	item.Kind = "session_divider"
	item.Summary = "Session changed: " + p.Reason
	if raw {
		item.Summary = "Session changed: " + p.Cause
		item.Session = p.PreviousSessionID
		item.Agent = recordedAgent(p.PreviousAgent)
		item.Shown = p.Cause != "idle_sleep" && p.Cause != "daemon_restart"
	} else {
		item.Shown = reason != "idle_evict"
		item.Session = p.NewSessionID
	}
	f.lastBoundary = e.ID
	f.successor = history.SessionProvenance{}
	if reason != "idle_evict" && p.NewSessionID != "" {
		f.successor.SessionID = p.NewSessionID
		if raw {
			f.successor.Kind = recordedAgent(p.NextAgent)
		} else if e.Session != nil && e.Session.SessionID == p.NewSessionID {
			f.successor.Kind = recordedAgent(e.Session.Kind)
		}
	}
	if !raw || p.Cause != "daemon_restart" {
		f.legacyScope++
	}
	return true
}
