package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const (
	// seedChannelName and seedWorkspaceLabel are what a new host starts with
	// (#2569). Ordinary values once written: the operator can rename, archive or
	// delete the channel and relabel the workspace, and neither is recreated.
	seedChannelName    = "General"
	seedWorkspaceLabel = "Default workspace"
)

// seedDefaultWorkspace gives a new host a starting point once: a General channel
// in root/default, with that workspace labelled Default workspace, and a marker
// in the registry so it never happens again. A registry that already holds
// conversations gets the marker and nothing else, so an existing host is left as
// it is.
//
// create is channelCreator's return value, so the folder is confined to $HOME,
// created, trust-marked and bound to a session without a spawn exactly as
// `pyry channel new` does it. The label is keyed on the cwd read back from the
// created row, never on a rebuilt path: a workspace_labels key must byte-equal a
// stored cwd, and the stored one is the realpath.
//
// root is relay.WorkspaceRoot() in production, passed in so tests choose it.
//
// Every failure logs a static event — a stage, never a path or an error value,
// because Save's errors name the registry path and the creator's name the
// folder — and returns without the marker, so the next start retries. The
// daemon keeps running. A failed final Save leaves the marker set in memory
// beside the row and label, so a later lazy Save persists all three together;
// either way no restart can see a General on disk without being able to tell it
// needs no second one.
func seedDefaultWorkspace(reg *conversations.Registry, registryPath, root string, create func(cwd, name string) (string, error), logger *slog.Logger) {
	if reg.Seeded() {
		return
	}
	if len(reg.List()) > 0 {
		reg.MarkSeeded()
		if err := reg.Save(registryPath); err != nil {
			logger.Error("conversations: recording the seed marker failed",
				"event", "conversations.seed_mark_failed")
			return
		}
		logger.Info("conversations: existing host, no starting channel seeded",
			"event", "conversations.seed_skipped_existing")
		return
	}
	if root == "" {
		logSeedFailed(logger, "root")
		return
	}
	id, err := create(filepath.Join(root, "default"), seedChannelName)
	if err != nil {
		logSeedFailed(logger, "create")
		return
	}
	got, ok := reg.Get(conversations.ConversationID(id))
	if !ok {
		logSeedFailed(logger, "readback")
		return
	}
	label := seedWorkspaceLabel
	reg.SetWorkspaceLabel(got.Cwd, &label)
	reg.MarkSeeded()
	if err := reg.Save(registryPath); err != nil {
		logSeedFailed(logger, "save")
		return
	}
	logger.Info("conversations: starting channel seeded",
		"event", "conversations.seeded",
		"conversation_id", id)
}

func logSeedFailed(logger *slog.Logger, stage string) {
	logger.Error("conversations: seeding the starting channel failed",
		"event", "conversations.seed_failed",
		"stage", stage)
}

// seedWhenReady runs seed on its own goroutine once ready closes, or never if
// ctx ends first, and returns a channel closed when that goroutine exits. ready
// is Pool.Ready(): before it closes Pool.Mint persists a session and then
// returns ErrPoolNotRunning, which the creator reads as a failed mint, so a seed
// run any earlier would fail on every start and orphan a session each time.
func seedWhenReady(ctx context.Context, ready <-chan struct{}, seed func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ready:
			seed()
		case <-ctx.Done():
		}
	}()
	return done
}
