package memoryruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompatibilityProbeProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		return
	}
	args := os.Args[3:]
	switch {
	case len(args) == 5 && args[2] == "-c":
		_, _ = os.Stdout.WriteString(`{"python":"3.12.11","pip":"24.3.1"}`)
	case len(args) == 4 && filepath.Base(args[2]) == "memsearch" && args[3] == "--version":
		_, _ = os.Stdout.WriteString("memsearch, version 0.4.21\n")
	case strings.Join(args, " ") == "-I -B -m pip --isolated check":
		if _, err := os.Stat("block-check"); err == nil {
			if err := os.WriteFile("check-started", []byte("yes"), 0600); err != nil {
				os.Exit(2)
			}
			for {
				time.Sleep(time.Hour)
			}
		}
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func TestCompatibilityProbeCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"cancel", context.Canceled},
		{"deadline", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, o, hits := testEngine(t)
			e.probe = probe
			base := e.provision
			attempts := 0
			generation := make(chan string, 1)
			e.provision = func(ctx context.Context, dir string, p lock) error {
				if err := base(ctx, dir, p); err != nil {
					return err
				}
				binary := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
				script := "#!/bin/sh\nexec " + binary + " -test.run=^TestCompatibilityProbeProcess$ -- \"$@\"\n"
				if err := os.WriteFile(filepath.Join(dir, "python/bin", pythonName(p.Python)), []byte(script), 0700); err != nil {
					return err
				}
				attempts++
				if attempts == 1 {
					if err := os.WriteFile(filepath.Join(dir, "block-check"), nil, 0600); err != nil {
						return err
					}
					generation <- dir
				}
				return nil
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if tc.want == context.DeadlineExceeded {
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			type outcome struct {
				runtime Runtime
				err     error
			}
			done := make(chan outcome, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				r, err := e.install(ctx, o)
				done <- outcome{r, err}
			}()
			t.Cleanup(func() { cancel(); <-finished })
			var dir string
			select {
			case dir = <-generation:
			case result := <-done:
				t.Fatalf("installer stopped before probe: %v", result.err)
			case <-time.After(10 * time.Second):
				t.Fatal("installer did not reach probe")
			}
			testWaitFile(t, filepath.Join(dir, "check-started"))
			if tc.want == context.Canceled {
				cancel()
			}
			result := <-done
			if !errors.Is(result.err, tc.want) || !errors.Is(result.err, ErrProbe) {
				t.Fatalf("compatibility probe lost cause: %v; want %v and ErrProbe", result.err, tc.want)
			}
			var failure *Failure
			if !errors.As(result.err, &failure) || failure.Stage != "verify" {
				t.Fatalf("unexpected failure stage: %v", result.err)
			}
			if result.runtime.Launch != "" || result.runtime.Python != "" {
				t.Fatal("aborted setup returned a launch location")
			}
			testNoPublished(t, o)
			retry := testInstall(t, e, o)
			if retry.Versions["python"] != e.pins.Python || retry.Versions["pip"] != e.pins.Bootstrap {
				t.Fatal("retry did not verify pinned versions")
			}
			for _, path := range []string{retry.Launch, filepath.Join(o.Home, managed, "current.json")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("retry did not publish a usable runtime: %v", err)
				}
			}
			if hits.Load() != 1 {
				t.Fatalf("retry did not reuse verified download: %d", hits.Load())
			}
		})
	}
}
