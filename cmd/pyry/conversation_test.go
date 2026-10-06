package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestParseConversationNewArgs(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tt := range []struct {
		args []string
		want control.ConversationPayload
		bad  bool
	}{
		{want: control.ConversationPayload{Type: str("chat")}},
		{args: []string{"--type", "channel", "--name", "Work", "--model", "sonnet", "--effort", "max"}, want: control.ConversationPayload{Type: str("channel"), Name: "Work", Model: str("sonnet"), Effort: str("max")}},
		{args: []string{"--model=", "--effort", ""}, want: control.ConversationPayload{Type: str("chat"), Model: str(""), Effort: str("")}},
		{args: []string{"--type="}, bad: true},
		{args: []string{"--type", "other"}, bad: true},
		{args: []string{"extra"}, bad: true},
		{args: []string{"--unknown"}, bad: true},
		{args: []string{"--name"}, bad: true},
		{args: []string{"--model"}, bad: true},
		{args: []string{"--model", "--effort=low"}, bad: true},
		{args: []string{"--effort"}, bad: true},
		{args: []string{"--type"}, bad: true},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			got, err := parseConversationNewArgs(tt.args)
			if (err != nil) != tt.bad || !tt.bad && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parse = %#v, %v; want %#v, bad=%v", got, err, tt.want, tt.bad)
			}
		})
	}
}

type conversationMinterFunc func(context.Context, string, string, string, handlers.CreateSettings) (string, string, error)

func (f conversationMinterFunc) Create(ctx context.Context, label, cwd, agent string, settings handlers.CreateSettings) (string, string, error) {
	return f(ctx, label, cwd, agent, settings)
}

func TestConversationCreator_NamesPersistenceAndAnnouncement(t *testing.T) {
	for _, tt := range []struct{ kind, name, wantName string }{
		{"chat", "", ""}, {"chat", "Custom", "Custom"},
		{"channel", "", "real-project"}, {"channel", "Custom", "Custom"},
	} {
		t.Run(tt.kind+tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			trustCalls := installIdentityTrustMark(t)
			real := filepath.Join(home, "real-project")
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(home, "alias")
			if err := os.Symlink(real, alias); err != nil {
				t.Fatal(err)
			}
			resolved, err := filepath.EvalSymlinks(real)
			if err != nil {
				t.Fatal(err)
			}
			reg, path := newChannelTestRegistry(t, home)
			workspaceLabel := "Workspace"
			reg.SetWorkspaceLabel(resolved, &workspaceLabel)
			var announced []protocol.ConversationUpdatedPayload
			minter := conversationMinterFunc(func(_ context.Context, label, cwd, agent string, settings handlers.CreateSettings) (string, string, error) {
				if label == "" || cwd != alias || agent != protocol.AgentClaude || settings.Model != nil || settings.Effort != nil {
					t.Fatalf("unexpected mint inputs")
				}
				dir, err := resolveSpawnDir(cwd)
				return "bound", dir, err
			})
			create := conversationCreator(reg, minter, path, func(p protocol.ConversationUpdatedPayload) { announced = append(announced, p) }, discardLogger())
			id, err := create(alias, tt.name, tt.kind, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := reg.Get(conversations.ConversationID(id))
			if !ok || got.Cwd != resolved || got.CurrentSessionID != "bound" || got.IsPromoted != (tt.kind == "channel") || *trustCalls != 1 {
				t.Fatalf("row = %+v, trust calls=%d", got, *trustCalls)
			}
			if tt.wantName == "" && got.Name != nil || tt.wantName != "" && (got.Name == nil || *got.Name != tt.wantName) {
				t.Fatalf("name = %v, want %q", got.Name, tt.wantName)
			}
			if saved := readSavedConversations(t, path); len(saved) != 1 || !reflect.DeepEqual(saved[0], got) {
				t.Fatalf("saved = %+v, want %+v", saved, got)
			}
			if len(announced) != 1 || announced[0].ID != id || announced[0].Cwd != got.Cwd || !reflect.DeepEqual(announced[0].Name, got.Name) || announced[0].IsPromoted != got.IsPromoted || announced[0].WorkspaceLabel == nil || *announced[0].WorkspaceLabel != "Workspace" {
				t.Fatalf("announcement = %+v", announced)
			}
		})
	}
}

func TestConversationCreator_Settings(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tt := range []struct {
		name                           string
		model, effort                  *string
		have                           bool
		wantErr, wantModel, wantEffort string
	}{
		{name: "omitted", have: true, wantModel: "opus", wantEffort: "low"},
		{name: "accepted", have: true, model: str("sonnet"), effort: str("max"), wantModel: "sonnet", wantEffort: "max"},
		{name: "reset model", have: true, model: str(""), wantEffort: "low"},
		{name: "reset effort", have: true, effort: str(""), wantModel: "opus"},
		{name: "reset both", model: str(""), effort: str("")},
		{name: "shape model", model: str("private\nmodel"), wantErr: "invalid conversation settings"},
		{name: "shape effort", effort: str("private\neffort"), wantErr: "invalid conversation settings"},
		{name: "absent model", have: true, model: str("private-model"), wantErr: "requested model is not offered"},
		{name: "unavailable model", model: str("private-model"), wantErr: "model list is unavailable"},
		{name: "effort default model", have: true, effort: str("max"), wantErr: "requested effort is not offered"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			trustCalls := installIdentityTrustMark(t)
			pool, _ := newDormantWritePool(t, "")
			opus, low := "opus", "low"
			if err := pool.UpdateSettings(dormantWriteBootID, sessions.SettingsUpdate{Model: &opus, Effort: &low}); err != nil {
				t.Fatal(err)
			}
			runPoolReady(t, pool)
			reg, path := newChannelTestRegistry(t, home)
			create := conversationCreator(reg, sessionMinter{pool, &agentVocabularyDouble{list: claudeEntries, have: tt.have}}, path, nil, discardLogger())
			cwd := filepath.Join(home, "not-yet-created")
			id, err := create(cwd, "", "chat", tt.model, tt.effort)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr || id != "" || len(reg.List()) != 0 || len(pool.List()) != 1 || *trustCalls != 0 {
					t.Fatalf("refusal = %q, %v; rows=%d sessions=%d trust=%d", id, err, len(reg.List()), len(pool.List()), *trustCalls)
				}
				if _, err := os.Stat(cwd); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("workspace side effect: %v", err)
				}
				// A competing confinement error proves settings validation runs first.
				if _, err := create("/", "", "chat", tt.model, tt.effort); err == nil || err.Error() != tt.wantErr {
					t.Fatalf("ordering: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			row, _ := reg.Get(conversations.ConversationID(id))
			got, err := pool.SettingsFor(sessions.SessionID(row.CurrentSessionID))
			if err != nil || got.Model != tt.wantModel || got.Effort != tt.wantEffort {
				t.Fatalf("settings = %+v, %v", got, err)
			}
			for _, info := range pool.List() {
				if string(info.ID) == row.CurrentSessionID && info.LifecycleState.String() != "evicted" {
					t.Fatalf("mint activated: %+v", info)
				}
			}
		})
	}
}

func TestConversationCreator_Failures(t *testing.T) {
	for _, tt := range []struct {
		name, cwd, kind, want string
		mintErr               error
		saveFail              bool
	}{
		{name: "empty cwd", kind: "chat", want: "working directory not allowed"},
		{name: "bad type", cwd: "raw", kind: "", want: "invalid conversation type"},
		{name: "outside home", cwd: "/", kind: "chat", mintErr: handlers.ErrSpawnDirRejected, want: "working directory not allowed"},
		{name: "trust error", cwd: "raw", kind: "chat", mintErr: errors.New("private workspace detail"), want: "could not start conversation session"},
		{name: "mint error", cwd: "raw", kind: "chat", mintErr: errors.New("private mint detail"), want: "could not start conversation session"},
		{name: "best effort save", cwd: "raw", kind: "chat", saveFail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg, path := newChannelTestRegistry(t, t.TempDir())
			if tt.saveFail {
				path = t.TempDir()
			}
			calls := 0
			minter := conversationMinterFunc(func(context.Context, string, string, string, handlers.CreateSettings) (string, string, error) {
				calls++
				return "bound", "/resolved", tt.mintErr
			})
			id, err := conversationCreator(reg, minter, path, nil, discardLogger())(tt.cwd, "", tt.kind, nil, nil)
			if tt.want != "" {
				if id != "" || err == nil || err.Error() != tt.want || len(reg.List()) != 0 {
					t.Fatalf("refusal = %q, %v; rows=%d", id, err, len(reg.List()))
				}
				if (tt.cwd == "" || tt.kind == "") && calls != 0 {
					t.Fatal("invalid boundary reached minter")
				}
			} else if err != nil || id == "" || len(reg.List()) != 1 {
				t.Fatalf("save failure refused create: %q %v", id, err)
			}
		})
	}
}

func TestConversationCLI_DeletedCwd(t *testing.T) {
	if os.Getenv("PYRY_TEST_DELETED_CWD") == "1" {
		dir := os.Getenv("PYRY_TEST_CWD")
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if err := os.Remove(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if err := runArgs([]string{"pyry", "conversation", "-pyry-socket=/missing.sock", "new"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	dir := filepath.Join(t.TempDir(), "cwd")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConversationCLI_DeletedCwd$")
	cmd.Env = append(os.Environ(), "PYRY_TEST_DELETED_CWD=1", "PYRY_TEST_CWD="+dir)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "resolve current directory") {
		t.Fatalf("deleted cwd = %v, stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
}
