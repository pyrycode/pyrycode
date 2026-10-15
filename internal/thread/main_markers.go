package thread

// mainCallMarker keeps only the facts needed by a possible later child claim.
// Ordinary duplicate facts cannot mutate a finished call. Item -2 denotes a
// launcher, which has no ordinary item. Outcomes reference history, not content.
type mainCallMarker struct {
	item     int
	scope    uint64
	lifetime string
	terminal *mainOutcome
}

func (st *mainTurn) markerGroup(m mainCallMarker) agentKey {
	return agentKey{source: st.key.source, tagged: st.key.tagged, scope: m.scope, lifetime: m.lifetime}
}

// compactMainCall releases finished ordinary owners and their empty map.
// Shell owners remain shared with agentCall: a later task link can reopen them.
// Uncreated calls retain their pending outcomes until their kind is known.
func (f *Fold) compactMainCall(st *mainTurn, id string) {
	if f.source == nil {
		return
	}
	call := st.calls[id]
	if call == nil || call.shell != nil || call.terminal == nil || (call.item < 0 && !call.launcher) {
		return
	}
	item := call.item
	if call.launcher {
		item = -2
	}
	if st.finished == nil {
		st.finished = make(map[string]mainCallMarker)
	}
	st.finished[id] = mainCallMarker{item, call.group.scope, call.group.lifetime, call.terminal}
	delete(st.calls, id)
	if len(st.calls) == 0 {
		st.calls = nil
	}
}

// takeMainCall transfers intrinsic ordinary evidence to a child lane. A compact
// marker never owns a mutable call; reconstruct only when a child claims it.
func (st *mainTurn) takeMainCall(id string, group agentKey) *mainCall {
	call := st.calls[id]
	if call != nil && call.group == group {
		delete(st.calls, id)
		if len(st.calls) == 0 {
			st.calls = nil
		}
		return call
	}
	m, ok := st.finished[id]
	if !ok || st.markerGroup(m) != group {
		return nil
	}
	delete(st.finished, id)
	if len(st.finished) == 0 {
		st.finished = nil
	}
	call = &mainCall{group: group, item: m.item, terminal: m.terminal}
	if m.item == -2 {
		call.item, call.launcher = -1, true
	}
	return call
}
