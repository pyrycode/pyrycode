// Command pyry is the Pyrycode daemon — a process supervisor for Claude Code.
//
// Usage:
//
//	pyry                Start a supervised Claude Code session in the foreground
//	pyry version        Print version and exit
//	pyry status         Query the running daemon via its control socket
//
// See https://github.com/pyrycode/pyrycode for documentation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

// defaultSocketPath returns ~/.pyry/pyry.sock with $HOME expanded. If $HOME
// can't be resolved we fall back to a CWD-relative path so error messages
// stay helpful.
func defaultSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "pyry.sock"
	}
	return filepath.Join(home, ".pyry", "pyry.sock")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pyry:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println("pyry", Version)
			return nil
		case "status":
			return runStatus(os.Args[2:])
		case "help", "-h", "--help":
			printHelp()
			return nil
		}
	}

	return runSupervisor(os.Args[1:])
}

// runSupervisor starts the supervisor and the control server together, blocks
// until the context is cancelled by SIGINT/SIGTERM, then drains both.
func runSupervisor(args []string) error {
	fs := flag.NewFlagSet("pyry", flag.ContinueOnError)
	claudeBin := fs.String("claude", "claude", "path to the claude binary")
	workdir := fs.String("workdir", "", "working directory for claude (default: current)")
	resume := fs.Bool("resume", true, "resume the most recent session on restart")
	verbose := fs.Bool("verbose", false, "verbose logging")
	socketPath := fs.String("socket", defaultSocketPath(), "control socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg := supervisor.Config{
		ClaudeBin:  *claudeBin,
		WorkDir:    *workdir,
		ResumeLast: *resume,
		ClaudeArgs: fs.Args(),
		Logger:     logger,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	sup, err := supervisor.New(cfg)
	if err != nil {
		return fmt.Errorf("supervisor init: %w", err)
	}

	ctrl := control.NewServer(*socketPath, sup, logger)
	if err := ctrl.Listen(); err != nil {
		return fmt.Errorf("control listen: %w", err)
	}
	defer func() { _ = ctrl.Close() }()

	ctrlDone := make(chan error, 1)
	go func() { ctrlDone <- ctrl.Serve(ctx) }()

	logger.Info("pyrycode starting",
		"version", Version,
		"claude", cfg.ClaudeBin,
		"socket", *socketPath,
	)
	supErr := sup.Run(ctx)

	// Stop the control server (already wired to ctx but Close is idempotent
	// and ensures the socket file is gone before we return).
	_ = ctrl.Close()
	<-ctrlDone

	if supErr != nil && !errors.Is(supErr, context.Canceled) {
		return fmt.Errorf("supervisor: %w", supErr)
	}
	logger.Info("pyrycode stopped")
	return nil
}

// runStatus implements the `pyry status` subcommand: dial the control socket,
// fetch a status snapshot, pretty-print it.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("pyry status", flag.ContinueOnError)
	socketPath := fs.String("socket", defaultSocketPath(), "control socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := control.Status(ctx, *socketPath)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}

	fmt.Printf("Phase:         %s\n", resp.Phase)
	if resp.ChildPID > 0 {
		fmt.Printf("Child PID:     %d\n", resp.ChildPID)
	}
	fmt.Printf("Restart count: %d\n", resp.RestartCount)
	if resp.LastUptime != "" {
		fmt.Printf("Last uptime:   %s\n", resp.LastUptime)
	}
	if resp.NextBackoff != "" {
		fmt.Printf("Next backoff:  %s\n", resp.NextBackoff)
	}
	fmt.Printf("Started at:    %s\n", resp.StartedAt)
	fmt.Printf("Uptime:        %s\n", resp.Uptime)
	return nil
}

func printHelp() {
	fmt.Print(`pyry — Pyrycode daemon, a supervisor for Claude Code

Usage:
  pyry [flags] [-- claude args...]   start a supervised claude session
  pyry status [flags]                query the running daemon
  pyry version                       print version
  pyry help                          show this help

Supervisor flags:
  -claude string   path to the claude binary (default "claude")
  -workdir string  working directory for claude (default: current)
  -resume          resume the most recent session on restart (default true)
  -verbose         verbose logging
  -socket string   control socket path (default ~/.pyry/pyry.sock)

Status flags:
  -socket string   control socket path (default ~/.pyry/pyry.sock)

Examples:
  pyry                                # run claude under supervision
  pyry -verbose                       # with debug logging
  pyry -- --channels plugin:discord   # pass args through to claude
  pyry status                         # check on the running daemon

See https://github.com/pyrycode/pyrycode for documentation.
`)
}
