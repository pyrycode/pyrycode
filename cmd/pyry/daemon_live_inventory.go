package main

import (
	"bytes"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Inventory evidence follows producer caches across clears. It is separate from
// current readings: consulting it never reassigns its producing generation.
type liveInventoryKey struct{ conversation, family string }
type liveInventoryCache struct {
	payload json.RawMessage
	source  daemonLiveSource
}

func (o *daemonLiveState) cacheInventoryLocked(src daemonLiveSource, ev turnevent.Event) {
	switch ev.(type) {
	case turnevent.ModelList, turnevent.SlashCommandList:
	default:
		return
	}
	if src.SessionGeneration == 0 || o.conversations[src.ConversationID] == nil {
		return
	}
	typ, payload, ok := turnbridge.MapEvent(ev, turnbridge.TurnContext{})
	if !ok {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if o.inventories == nil {
		o.inventories = make(map[liveInventoryKey]liveInventoryCache)
	}
	key := liveInventoryKey{src.ConversationID, typ}
	previous, found := o.inventories[key]
	if found && previous.source.SessionGeneration > src.SessionGeneration {
		return
	}
	o.inventories[key] = liveInventoryCache{raw, src}
}

// inventorySource validates the producer's unprojected menu against its cached
// evidence. Persisted bytes without evidence have unresolved provenance.
func (b *daemonLiveBindings) inventorySource(src daemonLiveSource, typ string, payload any) daemonLiveSource {
	original := src
	src.provenance = history.SessionProvenance{}
	if b == nil || b.sink == nil || b.sink.live == nil {
		return src
	}
	switch p := payload.(type) {
	case protocol.ModelListPayload:
		p.ConversationID = ""
		payload = p
	case protocol.SlashCommandListPayload:
		p.ConversationID = ""
		payload = p
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return src
	}
	o := b.sink.live
	o.mu.Lock()
	defer o.mu.Unlock()
	cache, ok := o.inventories[liveInventoryKey{original.ConversationID, typ}]
	if ok && cache.source.provenance.SessionID == original.provenance.SessionID && bytes.Equal(cache.payload, raw) {
		return cache.source
	}
	return src
}

// cachedModelVocabularySource follows the same bound/default/stored selection as
// retainedModelVocabulary, carrying evidence before capability projection.
func cachedModelVocabularySource(pool *sessions.Pool, saved savedModelVocabulary, b *daemonLiveBindings, src daemonLiveSource) (turnevent.ModelList, daemonLiveSource, bool) {
	var bound *sessions.Session
	if src.provenance.SessionID != "" {
		bound, _ = pool.Lookup(sessions.SessionID(src.provenance.SessionID))
	}
	for _, sess := range []*sessions.Session{bound, pool.Default()} {
		if sess == nil {
			continue
		}
		if lister, ok := sess.Runner().(interface {
			ModelListLive() (turnevent.ModelList, daemonLiveSource, bool)
		}); ok {
			if list, source, have := lister.ModelListLive(); have {
				return list, inventoryResultSource(src, source), true
			}
		} else if list, have := sessionRetainedModelList(sess); have {
			p, _ := mapModelList(list, "")
			lookup := src
			lookup.provenance.SessionID = string(sess.ID())
			return list, inventoryResultSource(src, b.inventorySource(lookup, protocol.TypeModelList, p)), true
		}
	}
	list, ok := savedModelList(saved)
	src.provenance = history.SessionProvenance{}
	return list, src, ok
}

func inventoryResultSource(target, cached daemonLiveSource) daemonLiveSource {
	if cached.SessionGeneration == 0 {
		target.provenance = history.SessionProvenance{}
		return target
	}
	if cached.ConversationID != target.ConversationID {
		cached.ConversationID = target.ConversationID
		cached.SessionGeneration = 0 // foreign cache cannot claim this conversation's generation
	}
	return cached
}
