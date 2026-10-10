package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const memoryCredentialDeadline = 10 * time.Second

var errMemoryStorage = errors.New("memory credential: unsafe or unavailable storage")
var errMemorySelection = errors.New("memory credential: invalid selection or credential")
var errMemorySave = errors.New("memory credential: save failed")

func runMemory(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) > 0 && args[0] == "credential" {
		return runMemoryCredential(ctx, args, os.Stdin, os.Stdout)
	}
	return runMemoryConfiguration(ctx, args, os.Stdout)
}
func runMemoryCredential(ctx context.Context, args []string, in *os.File, out io.Writer) error {
	if len(args) != 3 || args[0] != "credential" || args[2] != "openai" || (args[1] != "set" && args[1] != "status") {
		return errors.New("memory credential: expected credential set/status openai")
	}
	s, err := localMemoryCredentialStore()
	if err != nil {
		return err
	}
	if args[1] == "set" {
		ref, err := s.set(ctx, in)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct {
			Reference string `json:"reference"`
		}{ref})
	}
	configured, err := s.status(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Configured bool `json:"configured"`
	}{configured})
}
func localMemoryCredentialStore() (memoryCredentialStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return memoryCredentialStore{}, errMemoryStorage
	}
	return memoryCredentialStore{home: home}, nil
}
func resolveMemoryCredential(ctx context.Context, ref string) (string, error) {
	s, err := localMemoryCredentialStore()
	if err != nil {
		return "", err
	}
	return s.resolve(ctx, ref)
}

// readMemoryInput has one reader per descriptor; polling avoids a detached reader
// that could consume input or retain a secret after cancellation.
func readMemoryInput(ctx context.Context, in *os.File) (string, error) {
	data := make([]byte, 0, maxAccountTokenBytes+1)
	buf := make([]byte, maxAccountTokenBytes+1)
	fd := int(in.Fd())
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		p := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(p, 10)
		if err != nil && err != unix.EINTR {
			return "", errMemorySelection
		}
		if p[0].Revents == 0 {
			continue
		}
		n, err := unix.Read(fd, buf[:maxAccountTokenBytes+1-len(data)])
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return "", errMemorySelection
		}
		data = append(data, buf[:n]...)
		if len(data) > maxAccountTokenBytes {
			return "", errMemorySelection
		}
		if n == 0 {
			break
		}
	}
	token, failure := parseTokenBytes(data)
	if failure != "" {
		return "", errMemorySelection
	}
	return token, ctx.Err()
}
