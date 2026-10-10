package main

import (
	"encoding/json"
	"slices"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type liveSourceTurn struct {
	main       string
	lanes      map[string]string
	launchers  map[string]string
	tools      map[string]string
	childTools map[string]bool
	doneTools  map[string]bool
	doneTasks  map[string]bool
}

// liveStreamCapture travels with a queued event. IDs and ordering never consult
// a later routing binding, including when the same routing ID is reactivated.
type liveStreamCapture struct {
	source                       daemonLiveSource
	mainTurnID, parentID, laneID string
	updates                      []daemonLiveReading
}

func mintLiveTurn() string {
	id, err := conversations.NewID()
	if err != nil {
		return ""
	}
	return string(id)
}
func (o *daemonLiveState) acceptEvent(src daemonLiveSource, ev turnevent.Event) *liveStreamCapture {
	if o == nil || src.SessionGeneration == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.acceptEventLocked(src, ev)
}

func (o *daemonLiveState) acceptEventLocked(src daemonLiveSource, ev turnevent.Event) *liveStreamCapture {
	cap := &liveStreamCapture{source: src}
	c := o.conversations[src.ConversationID]
	if c == nil || c.generation != src.SessionGeneration || c.stopped {
		return cap
	}
	t := o.turns[src]
	if t == nil {
		t = &liveSourceTurn{lanes: make(map[string]string), launchers: make(map[string]string), tools: make(map[string]string), childTools: make(map[string]bool), doneTools: make(map[string]bool), doneTasks: make(map[string]bool)}
		o.turns[src] = t
	}
	if c := o.conversations[src.ConversationID]; c != nil && c.generation == src.SessionGeneration {
		for _, family := range streamLiveFamilies {
			if r := c.readings[liveReadingKey{family, ""}]; r != nil && r.Envelope.SessionStateCleared {
				cap.updates = append(cap.updates, detachLiveReading(*r))
			}
		}
	}
	parent, toolID := "", ""
	switch v := ev.(type) {
	case turnevent.TextChunk:
		parent = v.ParentToolCallID
	case turnevent.ThoughtChunk:
		parent = v.ParentToolCallID
	case turnevent.ThinkingProgress:
		parent = v.ParentToolCallID
	case turnevent.ToolStart:
		parent, toolID = v.ParentToolCallID, v.ToolCallID
	case turnevent.ToolUpdate:
		parent, toolID = v.ParentToolCallID, v.ToolCallID
	case turnevent.ToolCallDenied:
		toolID = v.ToolCallID
	case turnevent.ToolProgress:
		toolID = v.ToolCallID
	}
	scope := t.tools[toolID]
	child := parent != "" || t.childTools[toolID]
	if parent != "" {
		lane := t.lanes[parent]
		if lane == "" {
			lane = mintLiveTurn()
			t.lanes[parent] = lane
		}
		scope = t.launchers[parent]
		if scope == "" {
			scope = lane
		}
		cap.parentID, cap.laneID = parent, lane
	}
	opensMain := turnMarkFor(ev) == turnMarkOpen
	switch ev.(type) {
	case turnevent.ToolProgress, turnevent.ToolCallDenied:
		opensMain = true
	}
	if !child && opensMain && t.main == "" {
		t.main = mintLiveTurn()
	}
	if !child {
		scope = t.main
	}
	cap.mainTurnID = t.main
	if toolID != "" && scope != "" {
		t.tools[toolID] = scope
		t.childTools[toolID] = child
	}
	if v, ok := ev.(turnevent.ToolStart); ok && (v.Title == "Agent" || v.Title == "Task") {
		t.launchers[v.ToolCallID] = scope
	}
	admit := func(typ string, payload any, id, turn string) {
		if !slices.Contains(streamLiveFamilies, typ) {
			return
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		if r, ok := o.admitLocked(src, protocol.Envelope{Type: typ, Payload: raw}, id, turn); ok {
			cap.updates = append(cap.updates, r)
		}
	}
	activity := turnMarkFor(ev) == turnMarkOpen
	if activity {
		o.retireLocked(src, liveReadingKey{protocol.TypeStall, ""})
	}
	var phase turnbridge.TurnState
	switch ev.(type) {
	case turnevent.ThoughtChunk, turnevent.ThinkingProgress:
		if !child {
			phase = turnbridge.StateThinking
		}
	case turnevent.TextChunk, turnevent.ToolStart, turnevent.ToolUpdate:
		if !child {
			phase = turnbridge.StateResponding
		}
	case turnevent.TurnEnd:
		phase = turnbridge.StateIdle
	}
	if phase != "" {
		typ, payload := turnbridge.BuildTurnState(src.ConversationID, phase)
		admit(typ, payload, "", "")
	}
	id := ""
	switch v := ev.(type) {
	case turnevent.ToolProgress:
		id = v.ToolCallID
	case turnevent.BackgroundTaskProgress:
		id = v.TaskID
	}
	if v, ok := ev.(turnevent.ToolStart); ok {
		delete(t.doneTools, v.ToolCallID)
	}
	if v, ok := ev.(turnevent.BackgroundTaskStarted); ok {
		delete(t.doneTasks, v.TaskID)
	}
	blocked := false
	if v, ok := ev.(turnevent.ToolProgress); ok {
		blocked = t.doneTools[v.ToolCallID]
	}
	if v, ok := ev.(turnevent.BackgroundTaskProgress); ok {
		blocked = t.doneTasks[v.TaskID]
	}
	typ, payload, ok := turnbridge.MapEvent(ev, turnbridge.TurnContext{ConversationID: src.ConversationID, TurnID: scope})
	if ok && !blocked && !(child && typ == protocol.TypeThinkingProgress) {
		retainedScope := scope
		if child {
			retainedScope = "child:" + toolID
		}
		admit(typ, payload, id, retainedScope)
	}
	switch v := ev.(type) {
	case turnevent.ToolUpdate:
		if v.Status == turnevent.ToolStatusCompleted || v.Status == turnevent.ToolStatusFailed {
			o.retireLocked(src, liveReadingKey{protocol.TypeToolProgress, v.ToolCallID})
			delete(t.tools, v.ToolCallID)
			t.doneTools[v.ToolCallID] = true
		}
	case turnevent.ToolCallDenied:
		o.retireLocked(src, liveReadingKey{protocol.TypeToolProgress, v.ToolCallID})
		delete(t.tools, v.ToolCallID)
		t.doneTools[v.ToolCallID] = true
	case turnevent.BackgroundTaskUpdated:
		if v.Status != "" {
			o.retireLocked(src, liveReadingKey{protocol.TypeBackgroundTaskProgress, v.TaskID})
			t.doneTasks[v.TaskID] = true
		}
	case turnevent.TurnEnd:
		if c := o.conversations[src.ConversationID]; c != nil && c.generation == src.SessionGeneration {
			for key, turn := range c.scopes {
				if turn == t.main && (key.family == protocol.TypeThinkingProgress || key.family == protocol.TypeToolProgress) {
					o.retireLocked(src, key)
				}
			}
		}
		for id, turn := range t.tools {
			if turn == t.main && !t.childTools[id] {
				t.doneTools[id] = true
				delete(t.tools, id)
			}
		}
		t.main = ""
	}
	return cap
}

// closeProducer retires progress that cannot outlive its process. A delayed
// predecessor stop carries its original source and cannot clear a successor.
func (o *daemonLiveState) closeProducer(src daemonLiveSource) *liveStreamCapture {
	if o == nil || src.SessionGeneration == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	cap := o.acceptEventLocked(src, turnevent.TurnEnd{})
	if c := o.conversations[src.ConversationID]; c != nil && c.generation == src.SessionGeneration {
		c.stopped = true
		o.releaseSourcesLocked(src.ConversationID)
		for key := range c.readings {
			if key.family == protocol.TypeToolProgress || key.family == protocol.TypeThinkingProgress || key.family == protocol.TypeBackgroundTaskProgress {
				o.retireLocked(src, key)
			}
		}
	}
	return cap
}
