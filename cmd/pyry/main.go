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
	"syscall"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

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
			return errors.New("status: not yet implemented (Phase 0.2)")
		case "help", "-h", "--help":
			printHelp()
			return nil
		}
	}

	fs := flag.NewFlagSet("pyry", flag.ContinueOnError)
	claudeBin := fs.String("claude", "claude", "path to the claude binary")
	workdir := fs.String("workdir", "", "working directory for claude (default: current)")
	resume := fs.Bool("resume", true, "resume the most recent session on restart")
	verbose := fs.Bool("verbose", false, "verbose logging")
	if err := fs.Parse(os.Args[1:]); err != nil {
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

	logger.Info("pyrycode starting", "version", Version, "claude", cfg.ClaudeBin)
	if err := sup.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("supervisor: %w", err)
	}
	logger.Info("pyrycode stopped")
	return nil
}

func printHelp() {
	fmt.Print(`pyry — Pyrycode daemon, a supervisor for Claude Code

Usage:
  pyry [flags] [-- claude args...]
  pyry version
  pyry status
  pyry help

Flags:
  -claude string   path to the claude binary (default "claude")
  -workdir string  working directory for claude (default: current)
  -resume          resume the most recent session on restart (default true)
  -verbose         verbose logging

Examples:
  pyry                                # run claude under supervision
  pyry -verbose                       # with debug logging
  pyry -- --channels plugin:discord   # pass args through to claude

See https://github.com/pyrycode/pyrycode for documentation.
`)
}
