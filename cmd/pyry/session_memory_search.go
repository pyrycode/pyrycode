package main

import (
	"context"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/memorysearch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

type memorySearchLauncher interface {
	MemorySearchLaunch() (workspace string, childPATH *string, codexHome string, generation uint64, ok bool)
}

// A settings read must answer promptly even when its selected child is silent.
const memorySearchMCPQueryTimeout = 500 * time.Millisecond

func unknownMemorySearch() protocol.MemorySearchReport {
	return protocol.MemorySearchReport{Availability: string(memorysearch.Unknown), Providers: []protocol.MemorySearchProvider{}}
}

func memorySearchFor(
	convReg *conversations.Registry,
	pool *sessions.Pool,
	configPath string,
) func(context.Context, string, string) (protocol.MemorySearchReport, error) {
	if convReg == nil || pool == nil {
		return nil
	}
	return func(ctx context.Context, conversationID, sessionID string) (protocol.MemorySearchReport, error) {
		unknown := unknownMemorySearch()
		conv, ok := convReg.Get(conversations.ConversationID(conversationID))
		if !ok || sessionID == "" || conv.CurrentSessionID != sessionID {
			return unknown, nil
		}
		id := sessions.SessionID(sessionID)
		sess, err := pool.Lookup(id)
		if err != nil {
			return unknown, nil
		}
		agent, err := pool.HarnessFor(id)
		if err != nil {
			return unknown, nil
		}
		runner := sess.Runner()
		launcher, ok := runner.(memorySearchLauncher)
		if !ok {
			return unknown, nil
		}
		workspace, childPATH, codexHome, generation, ok := launcher.MemorySearchLaunch()
		if !ok || workspace == "" || (agent != "claude" && agent != "codex") {
			return unknown, nil
		}
		stillCurrent := func() bool {
			current, ok := convReg.Get(conv.ID)
			if !ok || current.CurrentSessionID != sessionID {
				return false
			}
			currentSession, err := pool.Lookup(id)
			if err != nil || currentSession != sess {
				return false
			}
			_, _, _, afterGeneration, live := launcher.MemorySearchLaunch()
			return live && afterGeneration == generation && ctx.Err() == nil
		}
		cfg, configErr := config.Load(configPath)
		launch := memorysearch.LaunchEvidence{
			Agent: agent, Workspace: workspace, CodexHome: codexHome,
			ChildPATH: childPATH, ChildRunsAsDaemon: true,
		}
		if agent == "claude" {
			if querier, ok := runner.(mcpStatusQuerier); ok {
				queryCtx, cancel := context.WithTimeout(ctx, memorySearchMCPQueryTimeout)
				status, complete := querier.QueryMCPStatus(queryCtx)
				cancel()
				if complete {
					launch.MCPStatus = &status
					launch.MCPStatusEffective = true
				}
			}
		}
		if !stillCurrent() {
			return unknown, nil
		}
		result, err := memorysearch.Detect(memorysearch.Input{
			Agent: agent, Workspace: workspace, CodexHome: codexHome,
			Config: cfg, ConfigErr: configErr, Launch: launch,
		})
		if err != nil {
			return unknown, nil
		}
		if !stillCurrent() {
			return unknown, nil
		}
		report := protocol.MemorySearchReport{
			Availability: string(result.Availability),
			Providers:    make([]protocol.MemorySearchProvider, 0, len(result.Providers)),
		}
		for _, provider := range result.Providers {
			report.Providers = append(report.Providers, protocol.MemorySearchProvider{
				ID: provider.ID, DisplayName: provider.DisplayName,
				Installed: provider.Installed, Enabled: provider.Enabled,
				Availability: string(provider.Availability),
			})
		}
		return report, nil
	}
}
