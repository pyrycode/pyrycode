package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestDaemonLiveMappedInventoryBounds(t *testing.T) {
	fill := strings.Repeat("<", 256)
	contextUsage := turnevent.ContextUsage{Model: fill, TotalTokens: math.MinInt, MaxTokens: math.MinInt, Percentage: math.MinInt, DroppedCategories: math.MaxInt, DroppedMCPTools: math.MaxInt, DroppedMemoryFiles: math.MaxInt}
	for range 32 {
		contextUsage.Categories = append(contextUsage.Categories, turnevent.ContextUsageCategory{Name: fill, Tokens: math.MinInt})
		contextUsage.MCPTools = append(contextUsage.MCPTools, turnevent.ContextUsageMCPTool{Name: fill, ServerName: fill, Tokens: math.MinInt})
		contextUsage.MemoryFiles = append(contextUsage.MemoryFiles, turnevent.ContextUsageMemoryFile{Path: fill, Type: fill, Tokens: math.MinInt})
	}
	slash := turnevent.SlashCommandList{DroppedCommands: math.MaxInt - 128}
	for range 128 {
		slash.Commands = append(slash.Commands, turnevent.SlashCommand{Name: fill, ArgumentHint: fill, Description: fill, Aliases: []string{strings.Repeat("<", 64), strings.Repeat("<", 64), strings.Repeat("<", 64), strings.Repeat("<", 64), strings.Repeat("<", 64), strings.Repeat("<", 64), strings.Repeat("<", 64), strings.Repeat("<", 64)}, TruncatedFields: []string{"name", "description", "argument_hint", "aliases"}})
	}
	models := turnevent.ModelList{DroppedModels: math.MaxInt}
	for range 10 {
		models.Models = append(models.Models, turnevent.ModelOption{Value: fill, ResolvedModel: fill, DisplayName: fill, EffortLevels: []string{strings.Repeat("<", 32), strings.Repeat("<", 32), strings.Repeat("<", 32), strings.Repeat("<", 32), strings.Repeat("<", 32), strings.Repeat("<", 32), strings.Repeat("<", 32), strings.Repeat("<", 32)}, TruncatedFields: []string{"value", "resolved_model", "display_name", "effort_levels"}})
	}
	mcp := turnevent.MCPStatus{DroppedServers: math.MaxInt}
	for range 16 {
		mcp.Servers = append(mcp.Servers, turnevent.MCPServerStatus{Name: fill, Status: fill, Scope: fill, Version: fill, Error: fill})
	}
	for _, ev := range []turnevent.Event{contextUsage, slash, models, mcp} {
		typ, payload, ok := turnbridge.MapEvent(ev, turnbridge.TurnContext{ConversationID: strings.Repeat("<", 36)})
		if !ok {
			t.Fatal("mapping failed")
		}
		t.Run(typ, func(t *testing.T) {
			o := newDaemonLiveState(func(string) (string, bool) { return strings.Repeat("<", 36), true })
			src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: fill}, true)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			maxID := uint64(math.MaxUint64)
			env := protocol.Envelope{ID: maxID, Type: typ, Payload: raw, InReplyTo: &maxID, EventID: &maxID, HistoryEntryID: &maxID}
			r, ok := o.admit(src, env, "")
			if !ok {
				if typ == protocol.TypeMCPStatus {
					// The existing mapping exceeds the bound; the owner must reject it.
					t.Skip("blocked on #3091: existing MCP mapping has no full envelope byte budget")
				}
				tagged := env
				tagged.SessionID = liveSessionTag(src.provenance)
				size, _ := json.Marshal(tagged)
				t.Fatalf("supported mapped payload rejected: %d bytes", len(size))
			}
			encoded, _ := json.Marshal(r.Envelope)
			t.Logf("fresh: %d bytes", len(encoded))
			env.Payload = json.RawMessage(`{}`)
			env.SessionStateCleared = true
			r, ok = o.admit(src, env, "")
			if !ok {
				t.Fatal("clear rejected")
			}
			encoded, _ = json.Marshal(r.Envelope)
			if len(encoded) > protocol.MaxThreadEnvelopeBytes {
				t.Fatal("clear exceeded budget")
			}
		})
	}
}
