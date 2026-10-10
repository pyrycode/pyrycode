package main

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"slices"
	"time"
)

var daemonLiveFamilies = append(append([]string(nil), streamLiveFamilies...),
	protocol.TypeModalShown, protocol.TypeQuestionShown, protocol.TypeResetting,
	protocol.TypeSessionError, protocol.TypeReplySuggestion, protocol.TypeSessionSettings)

func liveFamily(typ string) string {
	switch typ {
	case protocol.TypeModalDismissed:
		return protocol.TypeModalShown
	case protocol.TypeQuestionDismissed:
		return protocol.TypeQuestionShown
	case protocol.TypeSessionSettingsUpdated:
		return protocol.TypeSessionSettings
	}
	return typ
}
func liveDismissal(typ string) bool {
	return typ == protocol.TypeModalDismissed || typ == protocol.TypeQuestionDismissed
}

// daemonLiveOperation reserves ordering before asynchronous work. A completion
// keeps correlation fields but cannot overwrite a later reservation or update.
type daemonLiveOperation struct {
	owner    *daemonLiveState
	source   daemonLiveSource
	key      liveReadingKey
	revision uint64
}

func (o *daemonLiveState) begin(src daemonLiveSource, typ, id string) daemonLiveOperation {
	op := daemonLiveOperation{owner: o, source: src, key: liveReadingKey{liveFamily(typ), id}}
	if o == nil {
		return op
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if c := o.conversations[src.ConversationID]; c != nil && c.generation == src.SessionGeneration && (!c.stopped || liveControlFamily(typ)) {
		rk := liveRevisionKey{c.generation, op.key}
		c.revisions[rk]++
		op.revision = c.revisions[rk]
	}
	return op
}
func (op daemonLiveOperation) complete(e protocol.Envelope) (daemonLiveReading, bool) {
	if op.owner == nil || op.revision == 0 || liveFamily(e.Type) != op.key.family {
		return daemonLiveReading{}, false
	}
	e.SessionID = liveSessionTag(op.source.provenance)
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if !validDaemonLiveEnvelope(e) {
		return daemonLiveReading{}, false
	}
	r := daemonLiveReading{Envelope: detachLiveEnvelope(e), ConversationID: op.source.ConversationID, SessionGeneration: op.source.SessionGeneration, ReadingID: op.key.id, Revision: op.revision}
	o := op.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.conversations[op.source.ConversationID]
	if c == nil || (c.stopped && !liveControlFamily(e.Type)) || c.generation != op.source.SessionGeneration || c.revisions[liveRevisionKey{c.generation, op.key}] != op.revision {
		return detachLiveReading(r), false
	}
	if previous := c.readings[op.key]; previous != nil && previous.Revision == op.revision {
		return detachLiveReading(r), false
	}
	if liveDismissal(e.Type) {
		delete(c.readings, op.key)
	} else {
		c.readings[op.key] = &r
	}
	return detachLiveReading(r), true
}

func (op daemonLiveOperation) result(typ string, payload any) (daemonLiveReading, bool) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return daemonLiveReading{}, false
	}
	return op.complete(protocol.Envelope{Type: typ, Payload: raw})
}

// Attachments are installed before producers start. Captures take the same
// transition boundary as stream offers, without holding it across work or I/O.
type daemonLiveBindings struct {
	sink *streamTurnSink
	reg  *conversations.Registry
}

func (b *daemonLiveBindings) capture(conv, sid string, noSession bool) daemonLiveSource {
	if conv == "" || b == nil || b.sink == nil || b.sink.live == nil {
		return daemonLiveSource{}
	}
	b.sink.offerMu.Lock()
	defer b.sink.offerMu.Unlock()
	return b.captureLocked(conv, sid, noSession)
}
func (b *daemonLiveBindings) captureLocked(conv, sid string, noSession bool) daemonLiveSource {
	o := b.sink.live
	current := sid
	if b.reg != nil {
		row, ok := b.reg.Get(conversations.ConversationID(conv))
		if !ok {
			return daemonLiveSource{}
		}
		current = row.CurrentSessionID
	}
	p := history.SessionProvenance{}
	if sid != "" {
		p = history.SessionProvenance{SessionID: sid}
	} else if noSession {
		p.Kind = "none"
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.conversations[conv]
	if c == nil {
		c = o.newConversationLocked(conv, current)
	}
	if sid != "" && c.session != sid {
		return daemonLiveSource{}
	}
	return daemonLiveSource{ConversationID: conv, SessionGeneration: c.generation, provenance: p}
}
func (b *daemonLiveBindings) offer(src daemonLiveSource, typ string, payload any, id string, answerable ...bool) {
	if b == nil || b.sink == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	o := b.sink.live
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	r, ok := o.admitLocked(src, protocol.Envelope{Type: typ, Payload: raw}, id, "")
	if ok && len(answerable) > 0 {
		v := answerable[0]
		r.Answerable = &v
		o.conversations[src.ConversationID].readings[liveReadingKey{liveFamily(typ), id}] = &r
	}
}
func (b *daemonLiveBindings) dismiss(src daemonLiveSource, typ, id string) {
	b.offer(src, typ, json.RawMessage(`{}`), id)
}

func liveAttachment(attachments []*daemonLiveBindings) *daemonLiveBindings {
	if len(attachments) == 0 {
		return nil
	}
	return attachments[0]
}

// liveResolve holds the boundary only for synchronous binding resolution.
func liveResolve[T any](b *daemonLiveBindings, conv, typ string, resolve func() (T, bool)) (T, bool, daemonLiveOperation) {
	if b == nil || b.sink == nil || b.sink.live == nil || b.reg == nil {
		v, ok := resolve()
		return v, ok, daemonLiveOperation{}
	}
	b.sink.offerMu.Lock()
	defer b.sink.offerMu.Unlock()
	row, ok := b.reg.Get(conversations.ConversationID(conv))
	if !ok {
		var zero T
		return zero, false, daemonLiveOperation{}
	}
	src := b.captureLocked(string(row.ID), row.CurrentSessionID, row.CurrentSessionID == "")
	op := daemonLiveOperation{owner: b.sink.live, source: src}
	if typ != "" {
		op = b.sink.live.begin(src, typ, "")
	}
	v, ok := resolve()
	return v, ok, op
}

// cachedSource uses only matching retained bytes as evidence. A stored menu
// restored without source metadata stays unresolved, even with a current binding.
func (op daemonLiveOperation) cachedSource(payload any) daemonLiveOperation {
	if op.owner == nil {
		return op
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return op
	}
	op.owner.mu.Lock()
	defer op.owner.mu.Unlock()
	op.source.provenance = history.SessionProvenance{}
	if c := op.owner.conversations[op.source.ConversationID]; c != nil && c.generation == op.source.SessionGeneration {
		if r := c.readings[op.key]; r != nil && string(r.Envelope.Payload) == string(raw) {
			var sid *string
			if len(r.Envelope.SessionID) > 0 && json.Unmarshal(r.Envelope.SessionID, &sid) == nil {
				if sid == nil {
					op.source.provenance.Kind = "none"
				} else {
					op.source.provenance.SessionID = *sid
				}
			}
		}
	}
	return op
}

// liveSettingsUpdater preserves the legacy adapter and its constructor shape.
type liveSettingsUpdater struct {
	settingsUpdaterAdapter
	live *daemonLiveBindings
}

func (a liveSettingsUpdater) UpdateSettings(id string, u relay.SettingsUpdate) error {
	_, err := a.UpdateLive(id, u, protocol.Envelope{Type: protocol.TypeSessionSettingsUpdated})
	return err
}
func (a liveSettingsUpdater) UpdateLive(id string, u relay.SettingsUpdate, e protocol.Envelope) (daemonLiveReading, error) {
	if a.live == nil || a.live.sink == nil {
		return daemonLiveReading{}, a.settingsUpdaterAdapter.UpdateSettings(id, u)
	}
	conv, _ := conversationForSession(a.live.reg, id)
	src := a.live.capture(conv, id, false)
	op := a.live.sink.live.begin(src, protocol.TypeSessionSettingsUpdated, "")
	if err := a.settingsUpdaterAdapter.UpdateSettings(id, u); err != nil {
		return daemonLiveReading{}, err
	}
	raw, err := json.Marshal(protocol.SessionSettingsUpdatedPayload{SessionID: id})
	if err != nil {
		return daemonLiveReading{}, err
	}
	e.Type = protocol.TypeSessionSettingsUpdated
	e.Payload = raw
	reading, _ := op.complete(e)
	return reading, nil
}

func liveControlFamily(typ string) bool {
	return slices.Contains(daemonLiveFamilies[len(streamLiveFamilies):], liveFamily(typ)) || slices.Contains([]string{protocol.TypeContextUsage, protocol.TypeMCPStatus, protocol.TypeSlashCommandList, protocol.TypeModelList}, typ)
}

// liveInventoryReading exposes source-bearing menus without changing legacy
// provider signatures. The resolving closure keeps capability filtering intact.
func liveInventoryReading[T any](b *daemonLiveBindings, conv, typ string, e protocol.Envelope, resolve func() (T, bool)) (T, bool, daemonLiveReading) {
	payload, ok, op := liveResolve(b, conv, typ, resolve)
	if !ok {
		return payload, false, daemonLiveReading{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload, false, daemonLiveReading{}
	}
	e.Type = typ
	e.Payload = raw
	reading, _ := op.cachedSource(payload).complete(e)
	return payload, true, reading
}
func runSettingsReading(bound boundRunSettings, cfg relay.RunConfig, e protocol.Envelope) (daemonLiveReading, bool) {
	raw, err := json.Marshal(protocol.SessionSettingsPayload{SessionID: cfg.SessionID, Model: cfg.Model, Effort: cfg.Effort, YOLO: cfg.YOLO, PermissionMode: cfg.PermissionMode, UsedTokens: cfg.UsedTokens, WindowTokens: cfg.WindowTokens})
	if err != nil {
		return daemonLiveReading{}, false
	}
	e.Type = protocol.TypeSessionSettings
	e.Payload = raw
	return bound.liveOp.complete(e)
}

// next advances one continuing operation only while it still owns the family.
func (op daemonLiveOperation) next() daemonLiveOperation {
	if op.owner == nil || op.revision == 0 {
		return op
	}
	o := op.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.conversations[op.source.ConversationID]
	if c == nil || c.generation != op.source.SessionGeneration || c.revisions[liveRevisionKey{c.generation, op.key}] != op.revision {
		op.revision = 0
		return op
	}
	rk := liveRevisionKey{c.generation, op.key}
	c.revisions[rk]++
	op.revision = c.revisions[rk]
	return op
}
