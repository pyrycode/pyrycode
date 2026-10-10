package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

const memoryTranscriptInterval = 10 * time.Second

type memoryTranscriptHooks struct {
	ticks        <-chan time.Time
	beforeFeed   func(context.Context) error
	beforeRename func()
}

// Startup is shared with hermetic composition tests; export has no client,
// delivery, capture or indexer dependency. Cleanup cancels and joins all work.
func startMemoryTranscripts(parent context.Context, settings *config.MemorySettings, base string, h *history.Store, reg *conversations.Registry, log *slog.Logger, hooks ...memoryTranscriptHooks) func() {
	failure := func(reason string) {
		log.Warn("memory transcripts unavailable", "event", "memory_transcript.failure", "reason", reason)
	}
	if settings == nil {
		return func() {}
	}
	effective, err := resolveEffectiveMemory(parent, *settings, base)
	if err != nil {
		failure("settings")
		return func() {}
	}
	home, err := memoryHome()
	if err == nil {
		home, err = memoryReservedPath(home)
	}
	if err != nil || effective.TranscriptPath != filepath.Join(home, ".pyry", "memory", "recent-transcripts") {
		failure("storage")
		return func() {}
	}
	var hook memoryTranscriptHooks
	if len(hooks) > 0 {
		hook = hooks[0]
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var workers sync.WaitGroup
		defer workers.Wait()
		ticks := hook.ticks
		if ticks == nil {
			timer := time.NewTicker(memoryTranscriptInterval)
			defer timer.Stop()
			ticks = timer.C
		}
		active := make(map[conversations.ConversationID]chan struct{})
		for ctx.Err() == nil {
			for _, c := range reg.List() {
				if !conversations.ValidID(string(c.ID)) {
					continue
				}
				wake := active[c.ID]
				if wake == nil {
					wake = make(chan struct{}, 1)
					active[c.ID] = wake
					workers.Add(1)
					go func(id conversations.ConversationID, wake <-chan struct{}) {
						defer workers.Done()
						w := newMemoryTranscriptReader(h, id)
						for {
							select {
							case <-ctx.Done():
								return
							case <-wake:
							}
							if ctx.Err() != nil {
								return
							}
							err := w.reader.Walk(ctx, ^uint64(0), func(entries []history.Entry) error {
								if hook.beforeFeed != nil {
									if err := hook.beforeFeed(ctx); err != nil {
										return err
									}
								}
								return w.feed(entries)
							})
							if err != nil {
								if ctx.Err() != nil {
									return
								}
								failure("history")
								w = newMemoryTranscriptReader(h, id)
								continue
							}
							c, exists := reg.Get(id)
							if !exists {
								continue
							}
							for name, body := range w.files(c) {
								if err := publishMemoryTranscript(ctx, effective.TranscriptPath, name, body, hook.beforeRename); err != nil {
									if ctx.Err() != nil {
										return
									}
									failure("publication")
								}
							}
						}
					}(c.ID, wake)
				}
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticks:
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}
