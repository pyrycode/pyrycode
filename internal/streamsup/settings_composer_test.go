package streamsup

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComposeSettingsArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		args        []string
		wantCurrent string
		want        []string
	}{
		{name: "none appends", args: []string{"--model", "m"}, want: []string{"--model", "m", "--settings", "NEW"}},
		{name: "two-token replaced", args: []string{"--settings", "/s.json", "--model", "m"}, wantCurrent: "/s.json", want: []string{"--settings", "NEW", "--model", "m"}},
		{name: "joined replaced", args: []string{"--settings=/s.json"}, wantCurrent: "/s.json", want: []string{"--settings=NEW"}},
		{name: "last occurrence replaced", args: []string{"--settings", "/a.json", "--settings=/b.json", "--settings", "/c.json"}, wantCurrent: "/c.json", want: []string{"--settings", "/a.json", "--settings=/b.json", "--settings", "NEW"}},
		{name: "dangling flag appends", args: []string{"--settings"}, want: []string{"--settings", "--settings", "NEW"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.args)
			var seen string
			got, err := composeSettingsArgs(tc.args, func(current string) (string, error) {
				seen = current
				return "NEW", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if seen != tc.wantCurrent {
				t.Errorf("composer saw %q, want %q", seen, tc.wantCurrent)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("args = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(tc.args, original) {
				t.Error("input args mutated")
			}
		})
	}
	_, err := composeSettingsArgs([]string{"--settings", "/s.json"}, func(string) (string, error) {
		return "", errors.New("settings file /x: missing")
	})
	if err == nil || err.Error() != "streamsup: claude settings: settings file /x: missing" {
		t.Fatalf("err = %v", err)
	}
}

// settingsValue returns the value the child saw for its last --settings flag.
func settingsValue(args []string) string {
	v := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--settings" {
			v = args[i+1]
		}
	}
	return v
}

func TestSettingsComposerEveryLaunch(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"crash", "restart", "fresh"} {
		t.Run(action, func(t *testing.T) {
			var calls atomic.Int32
			r, out := tokenRunner(t, nil)
			r.cfg.SettingsComposer = func(current string) (string, error) {
				return fmt.Sprintf("composed-%d", calls.Add(1)), nil
			}
			cancel, join := runInBackground(t, r)
			defer func() { cancel(); join() }()
			first := awaitTokenWitness(t, out, 0)
			if got := settingsValue(first.Args); got != "composed-1" || !slices.Contains(first.Args, "--session-id") {
				t.Fatalf("first launch args: %q", first.Args)
			}
			writeTokenChild(t, r, "alive")
			waitForContains(t, out, "ALIVE", time.Second)
			flag := "--resume"
			switch action {
			case "crash":
				writeTokenChild(t, r, "crash")
			case "restart":
				r.Restart([]string{"--model", "new-model"})
			case "fresh":
				flag = "--session-id"
				r.RestartFresh("fresh-session")
			}
			second := awaitTokenWitness(t, out, 1)
			if got := settingsValue(second.Args); got != "composed-2" || !slices.Contains(second.Args, flag) {
				t.Fatalf("relaunch args: %q", second.Args)
			}
			if n := strings.Count(strings.Join(second.Args, " "), "--settings"); n != 1 {
				t.Fatalf("relaunch carries %d --settings flags: %q", n, second.Args)
			}
		})
	}
}

func TestSettingsComposerRefusalRetries(t *testing.T) {
	t.Parallel()
	rec := &logRecorder{}
	var calls, exits atomic.Int32
	r, out := tokenRunner(t, nil)
	r.cfg.Logger = slog.New(rec)
	r.log = r.cfg.Logger
	r.cfg.OnChildExit = func() { exits.Add(1) }
	r.cfg.SettingsComposer = func(string) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("settings file /x: not a JSON object")
		}
		return "/ok.json", nil
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	w := awaitTokenWitness(t, out, 0)
	if settingsValue(w.Args) != "/ok.json" {
		t.Fatalf("args after recovery: %q", w.Args)
	}
	if calls.Load() != 2 || exits.Load() != 1 || r.State().RestartCount != 1 {
		t.Fatal("refusal did not use the setup-failure retry lifecycle")
	}
	if strings.Count(out.String(), "\"Args\"") != 1 {
		t.Fatal("refused attempt launched a child")
	}
	refused := rec.withMessage("claude launch refused")
	if len(refused) != 1 || refused[0].attrs["err"] != "streamsup: claude settings: settings file /x: not a JSON object" {
		t.Fatalf("missing refusal diagnostic: %v", refused)
	}
}
