package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// conversationCreator owns the daemon boundary for conversation.new. The
// minter validates membership and resolves/trust-marks the workspace once,
// using the same defaults snapshot for validation and minting as paired clients.
func conversationCreator(reg *conversations.Registry, minter handlers.SessionCreator, registryPath string, announce func(protocol.ConversationUpdatedPayload), log *slog.Logger) func(cwd, name, kind string, model, effort *string) (string, error) {
	return func(cwd, name, kind string, model, effort *string) (string, error) {
		if kind != "chat" && kind != "channel" {
			return "", errors.New("invalid conversation type")
		}
		if cwd == "" {
			return "", errors.New(msgChannelCwdRejected)
		}
		if model != nil && !relay.ValidModel(*model) || effort != nil && !relay.ValidEffort(*effort) {
			log.Warn("control: conversation.new invalid settings", "event", "conversation_new.settings_rejected")
			return "", errors.New("invalid conversation settings")
		}
		id, err := conversations.NewID()
		if err != nil {
			log.Error("control: conversation.new id generation failed", "event", "conversation_new.id_failed", "err", err)
			return "", errors.New("could not start conversation session")
		}
		sessionID, resolved, err := minter.Create(context.Background(), string(id), cwd, protocol.AgentClaude, handlers.CreateSettings{Model: model, Effort: effort})
		if err != nil {
			// Membership errors carry no input. Never forward arbitrary minter
			// errors: confinement/trust/persistence errors can contain paths.
			switch {
			case errors.Is(err, relay.ErrModelNotOffered):
				return "", errors.New(relay.MsgSettingsModelNotOffered)
			case errors.Is(err, relay.ErrModelVocabularyUnavailable):
				return "", errors.New(relay.MsgModelListUnavailable)
			case errors.Is(err, relay.ErrEffortNotOffered):
				return "", errors.New("requested effort is not offered")
			case errors.Is(err, handlers.ErrSpawnDirRejected):
				log.Warn("control: conversation.new cwd rejected", "event", "conversation_new.cwd_rejected", "err", err)
				return "", errors.New(msgChannelCwdRejected)
			default:
				log.Warn("control: conversation.new mint failed", "event", "conversation_new.mint_failed", "err", err)
				return "", errors.New("could not start conversation session")
			}
		}
		var displayName *string
		if name == "" && kind == "channel" {
			name = filepath.Base(resolved)
		}
		if name != "" {
			displayName = &name
		}
		reg.Create(conversations.Conversation{
			ID: id, Name: displayName, Cwd: resolved, CurrentSessionID: sessionID,
			IsPromoted: kind == "channel", LastUsedAt: time.Now().UTC(),
		})
		// Creation remains successful if best-effort persistence fails, just
		// as handlers.CreateConversation and channelCreator treat it.
		if err := reg.Save(registryPath); err != nil {
			log.Error("control: conversation.new persist failed", "event", "conversation_new.persist_failed", "conversation_id", string(id), "err", err)
		}
		log.Info("control: conversation.new created", "event", "conversation_new.created", "conversation_id", string(id), "session_id", sessionID)
		if announce != nil {
			if got, ok := reg.Get(id); ok {
				var workspaceLabel *string
				if label, ok := reg.WorkspaceLabel(got.Cwd); ok {
					workspaceLabel = &label
				}
				announce(protocol.ConversationUpdatedPayload{
					ID: string(got.ID), Name: got.Name, Cwd: got.Cwd, WorkspaceLabel: workspaceLabel,
					IsPromoted: got.IsPromoted, IsArchived: got.IsArchived, IsMuted: got.IsMuted,
					ReadUpTo: got.ReadUpTo, LastUsedAt: got.LastUsedAt,
				})
			} else {
				log.Warn("control: conversation.new vanished before announcement", "event", "conversation_new.announce_row_vanished", "conversation_id", string(id))
			}
		}
		return string(id), nil
	}
}

// conversationSubmitter admits user turns through the ordinary inbound queue.
// Resolution validates the binding but leaves activation and retries to delivery.
func conversationSubmitter(reg *conversations.Registry, resolve func(string) (handlers.TurnWriter, error), enqueue func(string, string) uint64, registryPath string, log *slog.Logger) func(string, string) error {
	return func(id, text string) error {
		if _, err := resolve(id); err != nil {
			if errors.Is(err, conversations.ErrConversationNotFound) {
				return errors.New("unknown conversation")
			}
			return errors.New("conversation session is unavailable")
		}
		if enqueue(id, text) == 0 {
			return errors.New("conversation queue is full")
		}
		if reg.Update(conversations.ConversationID(id), func(c *conversations.Conversation) {
			c.LastUsedAt = time.Now().UTC()
		}) {
			if err := reg.Save(registryPath); err != nil {
				// Acceptance is final; persistence cannot revoke it. Keep caller
				// input and arbitrary downstream errors out of diagnostics.
				log.Warn("control: conversation.post last-used persist failed", "event", "conversation_post.last_used_persist_failed")
			}
		}
		return nil
	}
}

const conversationUsage = "usage: pyry conversation [-pyry-name=<instance>] [-pyry-socket=<path>] new [--type chat|channel] [--name LABEL] [--model MODEL] [--effort EFFORT]\n" +
	"       pyry conversation [-pyry-name=<instance>] [-pyry-socket=<path>] post --id ID (--text TEXT | --file PATH)"

func parseConversationPostArgs(args []string) (id, text, file string, err error) {
	fs := flag.NewFlagSet("pyry conversation post", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	idFlag := fs.String("id", "", "existing conversation id")
	textFlag := fs.String("text", "", "user message content")
	fileFlag := fs.String("file", "", "read user message from file")
	for i, arg := range args {
		name, _, hasValue := parseFlagSyntax(arg)
		if fs.Lookup(name) != nil && !hasValue && i+1 < len(args) && strings.HasPrefix(args[i+1], "-") {
			return "", "", "", fmt.Errorf("flag needs an argument: --%s", name)
		}
	}
	if err := fs.Parse(args); err != nil {
		return "", "", "", err
	}
	if fs.NArg() > 0 {
		return "", "", "", errors.New("unexpected positional argument")
	}
	if *idFlag == "" {
		return "", "", "", errors.New("--id is required")
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["text"] == set["file"] {
		return "", "", "", errors.New("exactly one of --text or --file is required")
	}
	return *idFlag, *textFlag, *fileFlag, nil
}

func runConversationPost(socketPath string, args []string) error {
	id, text, file, err := parseConversationPostArgs(args)
	if err != nil {
		return conversationUsageExit(err.Error())
	}
	body, err := channelPostContent(text, file)
	if err != nil {
		return fmt.Errorf("conversation post: %w", err)
	}
	if body == "" {
		return errors.New("conversation post: empty message")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := control.ConversationPost(ctx, socketPath, id, body); err != nil {
		return fmt.Errorf("conversation post: %w", err)
	}
	return nil
}

func parseConversationNewArgs(args []string) (control.ConversationPayload, error) {
	fs := flag.NewFlagSet("pyry conversation new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kind := fs.String("type", "chat", "chat or channel")
	name := fs.String("name", "", "display name")
	model := fs.String("model", "", "model (empty resets to the agent default)")
	effort := fs.String("effort", "", "effort (empty resets to the agent default)")
	// FlagSet accepts another flag as a string value. Require =value for
	// dash-prefixed values so a missing value stays a syntax error.
	for i, arg := range args {
		name, _, hasValue := parseFlagSyntax(arg)
		if fs.Lookup(name) != nil && !hasValue && i+1 < len(args) && strings.HasPrefix(args[i+1], "-") {
			return control.ConversationPayload{}, fmt.Errorf("flag needs an argument: --%s", name)
		}
	}
	if err := fs.Parse(args); err != nil {
		return control.ConversationPayload{}, err
	}
	if fs.NArg() > 0 {
		return control.ConversationPayload{}, fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	if *kind != "chat" && *kind != "channel" {
		return control.ConversationPayload{}, errors.New("--type must be chat or channel")
	}
	p := control.ConversationPayload{Type: kind, Name: *name}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "model":
			p.Model = model
		case "effort":
			p.Effort = effort
		}
	})
	return p, nil
}

func conversationUsageExit(detail string) error {
	fmt.Fprintln(os.Stderr, "pyry conversation:", detail)
	fmt.Fprintln(os.Stderr, conversationUsage)
	os.Exit(2)
	return nil // unreachable
}

func runConversation(args []string) error {
	// Check leading selector values before the shared string flag parser
	// can consume another flag as a value. Dash-prefixed values need =value.
	for i := 0; i < len(args); i++ {
		name, _, hasValue := parseFlagSyntax(args[i])
		if !clientPyryValueFlags[name] {
			break
		}
		if !hasValue {
			if i+1 < len(args) && strings.HasPrefix(args[i+1], "-") {
				return conversationUsageExit(fmt.Sprintf("flag needs an argument: -%s", name))
			}
			i++
		}
	}
	socketPath, rest, err := parseClientFlags("pyry conversation", args)
	if err != nil {
		return conversationUsageExit(err.Error())
	}
	if len(rest) == 0 {
		return conversationUsageExit("missing subcommand")
	}
	if rest[0] == "post" {
		return runConversationPost(socketPath, rest[1:])
	}
	if rest[0] != "new" {
		return conversationUsageExit(fmt.Sprintf("unknown verb %q", rest[0]))
	}
	p, err := parseConversationNewArgs(rest[1:])
	if err != nil {
		return conversationUsageExit(err.Error())
	}
	p.Cwd, err = currentDir()
	if err != nil {
		return fmt.Errorf("conversation new: resolve current directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := control.ConversationNew(ctx, socketPath, p)
	if err != nil {
		return fmt.Errorf("conversation new: %w", err)
	}
	fmt.Println(id)
	return nil
}

// currentDir returns the process's working directory, failing when that
// directory no longer exists. Linux getcwd fails on a deleted directory, but
// macOS returns the stale path, so the existence check makes both platforms
// refuse the same way before anything is sent to the daemon.
func currentDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(cwd); err != nil {
		return "", err
	}
	return cwd, nil
}
