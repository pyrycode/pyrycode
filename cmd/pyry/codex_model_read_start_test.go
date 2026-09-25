package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// startReadHarness runs one readCodexModelsAtStart against bin and a fresh
// Codex home and returns the log it wrote. The fake's thread and turn logs are
// armed so a test can prove the read opened neither.
func startReadHarness(t *testing.T, bin string, store *modelVocabularyStore) (logged string, threadLog, turnLog string) {
	t.Helper()
	tmp := t.TempDir()
	threadLog, turnLog = filepath.Join(tmp, "threads"), filepath.Join(tmp, "turns")
	t.Setenv("FAKECODEX_THREAD_LOG", threadLog)
	t.Setenv("FAKECODEX_TURN_LOG", turnLog)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := codexHarness{bin: bin, home: filepath.Join(tmp, "codex-home"), vocab: store}
	ctx, cancel := context.WithTimeout(context.Background(), codexStartTimeout)
	defer cancel()
	readCodexModelsAtStart(ctx, h, t.TempDir(), log)
	return buf.String(), threadLog, turnLog
}

// An empty store gains the fake's families from one read at start, which opens
// no thread, runs no turn, logs nothing and leaves Claude's list alone.
func TestStartCodexModelRead_FillsEmptyStore(t *testing.T) {
	store := newModelVocabularyStore(storePath(t))
	t.Cleanup(store.Close)
	claude := sentinelModelList("CLAUDE")
	store.Retain(claude)
	logged, threadLog, turnLog := startReadHarness(t, fakeCodexBin(t), store)
	if got := store.CodexModels(); !reflect.DeepEqual(got, fakeCodexFamilies) {
		t.Fatalf("CodexModels() = %#v\nwant %#v\nlog: %s", got, fakeCodexFamilies, logged)
	}
	if got, ok := store.ModelList(); !ok || !reflect.DeepEqual(got, claude) {
		t.Errorf("ModelList() = %#v, %v; want Claude's list unchanged", got, ok)
	}
	if logged != "" {
		t.Errorf("a successful read logged %q", logged)
	}
	for _, p := range []string{threadLog, turnLog} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			t.Errorf("read wrote %s: %q; want no thread and no turn", filepath.Base(p), b)
		}
	}
}

// A missing binary, an old Codex, a signed-out home and a failed model/list
// each leave the held entries as they were and log one Info line carrying no
// account detail and no model value.
func TestStartCodexModelRead_FailuresKeepHeldEntries(t *testing.T) {
	for _, tc := range []struct {
		name, env, val string
		bin            func(t *testing.T) string
	}{
		{name: "missing binary", bin: func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-codex") }},
		{name: "old version", env: "FAKECODEX_VERSION", val: "0.155.0"},
		{name: "signed out", env: "FAKECODEX_SIGNED_OUT", val: "1"},
		{name: "model list fails", env: "FAKECODEX_MODEL_LIST_FAIL", val: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(tc.env, tc.val)
			}
			bin := fakeCodexBin(t)
			if tc.bin != nil {
				bin = tc.bin(t)
			}
			store := newModelVocabularyStore(storePath(t))
			t.Cleanup(store.Close)
			held := sentinelCodexModels()
			store.RetainCodex(held)
			logged, _, _ := startReadHarness(t, bin, store)
			if got := store.CodexModels(); !reflect.DeepEqual(got, held) {
				t.Errorf("CodexModels() = %#v, want the held %#v", got, held)
			}
			lines := strings.Split(strings.TrimSpace(logged), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "level=INFO") {
				t.Fatalf("log = %q, want exactly one Info line", logged)
			}
			for _, secret := range []string{"@", "fakecodex@example.invalid", "gpt-"} {
				if strings.Contains(logged, secret) {
					t.Errorf("log %q carries %q", logged, secret)
				}
			}
			for _, m := range append(fakeCodexFamilies, held...) {
				if strings.Contains(logged, m.ResolvedModel) {
					t.Errorf("log %q carries model value %q", logged, m.ResolvedModel)
				}
			}
		})
	}
}

// The read never holds up the daemon's start: against a Codex that never
// answers, startCodexModelRead returns at once and wait cancels and joins it.
func TestStartCodexModelRead_DoesNotBlockStart(t *testing.T) {
	wrapper := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := newModelVocabularyStore(storePath(t))
	t.Cleanup(store.Close)
	h := codexHarness{bin: wrapper, home: filepath.Join(t.TempDir(), "codex-home"), vocab: store}
	began := time.Now()
	wait := startCodexModelRead(context.Background(), h, t.TempDir(), slog.New(slog.DiscardHandler))
	if d := time.Since(began); d > time.Second {
		t.Errorf("startCodexModelRead took %v, want it to return at once", d)
	}
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(codexStartTimeout):
		t.Fatal("wait did not cancel the read")
	}
	if got := store.CodexModels(); got != nil {
		t.Errorf("CodexModels() = %#v, want none", got)
	}
}

// AC3: on a daemon whose store held no Codex entry, a multi_agent client that
// asks for the model list after the read gets the fake's families tagged codex.
func TestStartCodexModelRead_MultiAgentClientGetsCodexEntries(t *testing.T) {
	pool, _ := newModelListTestPool(t)
	store := newModelVocabularyStore(storePath(t))
	t.Cleanup(store.Close)
	store.Retain(sentinelModelList("CLAUDE"))
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-start", LastUsedAt: time.Now().UTC()})

	before, _ := modelListFor(reg, pool, store)("conv-start", true)
	for _, m := range before.Models {
		if m.Agent == protocol.AgentCodex {
			t.Fatalf("store held a Codex entry before the read: %+v", m)
		}
	}
	startReadHarness(t, fakeCodexBin(t), store)
	got, ok := modelListFor(reg, pool, store)("conv-start", true)
	if !ok {
		t.Fatal("request refused; want a model_list")
	}
	var codex []protocol.ModelOption
	for _, m := range got.Models {
		if m.Agent == protocol.AgentCodex {
			codex = append(codex, m)
		}
	}
	if len(codex) != len(fakeCodexFamilies) {
		t.Fatalf("got %d Codex rows, want %d: %+v", len(codex), len(fakeCodexFamilies), got.Models)
	}
	for i, w := range fakeCodexFamilies {
		if codex[i].Value != w.Value || codex[i].Family != w.Value || codex[i].ResolvedModel != w.ResolvedModel {
			t.Errorf("Codex row %d = %+v, want family %q resolved %q", i, codex[i], w.Value, w.ResolvedModel)
		}
	}
}
