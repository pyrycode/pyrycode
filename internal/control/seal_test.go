package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestServerSealRetainsOwnership(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "control.sock")
	resolver := &fakeResolver{sess: &fakeSession{}}
	s := NewServer(sock, resolver, nil, nil, nil, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	s.SetChannelPoster(func(string, string) error { close(entered); <-release; return nil })
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()
	defer func() { s.Close(); <-served }()
	posted := make(chan error, 1)
	go func() { posted <- ChannelPost(ctx, sock, "channel", "text") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	sealed := make(chan struct{})
	go func() { s.Seal(); close(sealed) }()
	for {
		s.mu.Lock()
		ready := s.sealed
		s.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			close(release)
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-sealed:
		close(release)
		t.Fatal("seal did not join admitted writer")
	default:
	}
	next := NewServer(sock, resolver, nil, nil, nil, nil)
	if err := next.Listen(); !errors.Is(err, ErrInstanceRunning) {
		close(release)
		t.Fatalf("ownership lost: %v", err)
	}
	if err := ChannelPost(ctx, sock, "channel", "late"); err == nil {
		close(release)
		t.Fatal("sealed writer admitted")
	}
	close(release)
	select {
	case <-sealed:
	case <-ctx.Done():
		t.Fatal("seal did not join")
	}
	if err := <-posted; err != nil {
		t.Fatal(err)
	}
	s.Seal()
}
