package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	pairingSuccessSentinel = "PAIRING-BEARER-SUCCESS-7f3c1a9e"
	pairingErrorSentinel   = "PAIRING-PROVIDER-ERROR-4d8b2e6f"
)

type pairingCall struct {
	deviceLabel            string
	allowRemotePermissions bool
}

type fakePairingProvider struct {
	mu      sync.Mutex
	calls   []pairingCall
	pairing string
	err     error
}

func (f *fakePairingProvider) mint(deviceLabel string, allowRemotePermissions bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pairingCall{
		deviceLabel:            deviceLabel,
		allowRemotePermissions: allowRemotePermissions,
	})
	return f.pairing, f.err
}

func (f *fakePairingProvider) recordedCalls() []pairingCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pairingCall(nil), f.calls...)
}

func startServerWithPairingProvider(
	t *testing.T,
	provider func(deviceLabel string, allowRemotePermissions bool) (string, error),
	logger *slog.Logger,
) (sock string, stop func()) {
	t.Helper()
	sock = filepath.Join(shortTempDir(t), "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, logger, nil)
	srv.SetPairingProvider(provider)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	return sock, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("Serve did not return after cancel")
		}
	}
}

func TestMintPairing_WireRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		allowRemotePermissions bool
		wantRequest            string
	}{
		{
			name:                   "remote permission disabled remains explicit",
			allowRemotePermissions: false,
			wantRequest:            `{"verb":"pairing.mint","pairing":{"deviceLabel":"laptop","allowRemotePermissions":false}}` + "\n",
		},
		{
			name:                   "remote permission enabled",
			allowRemotePermissions: true,
			wantRequest:            `{"verb":"pairing.mint","pairing":{"deviceLabel":"laptop","allowRemotePermissions":true}}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestLine := make(chan string, 1)
			sock := startMisbehavingServer(t, func(conn net.Conn) {
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					requestLine <- "read request: " + err.Error()
					return
				}
				requestLine <- line
				_, _ = conn.Write([]byte(`{"pairing":{"pairing":"` + pairingSuccessSentinel + `"}}` + "\n"))
			})

			got, err := MintPairing(context.Background(), sock, "laptop", tt.allowRemotePermissions)
			if err != nil {
				t.Fatalf("MintPairing: %v", err)
			}
			if got != pairingSuccessSentinel {
				t.Errorf("pairing = %q, want %q", got, pairingSuccessSentinel)
			}
			if line := <-requestLine; line != tt.wantRequest {
				t.Errorf("request bytes = %q, want %q", line, tt.wantRequest)
			}
		})
	}

	got, err := json.Marshal(Response{Pairing: &PairingResult{Pairing: pairingSuccessSentinel}})
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}
	want := `{"pairing":{"pairing":"` + pairingSuccessSentinel + `"}}`
	if string(got) != want {
		t.Errorf("response bytes = %q, want %q", got, want)
	}
}

func TestServer_MintPairing_ForwardsExactlyOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		allowRemotePermissions bool
	}{
		{name: "remote permission disabled", allowRemotePermissions: false},
		{name: "remote permission enabled", allowRemotePermissions: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			provider := &fakePairingProvider{pairing: pairingSuccessSentinel}
			sock, stop := startServerWithPairingProvider(t, provider.mint, slog.New(slog.NewTextHandler(&logs, nil)))
			defer stop()

			got, err := MintPairing(context.Background(), sock, "desk-side tablet", tt.allowRemotePermissions)
			if err != nil {
				t.Fatalf("MintPairing: %v", err)
			}
			if got != pairingSuccessSentinel {
				t.Errorf("pairing = %q, want exact provider result", got)
			}
			wantCalls := []pairingCall{{
				deviceLabel:            "desk-side tablet",
				allowRemotePermissions: tt.allowRemotePermissions,
			}}
			if calls := provider.recordedCalls(); len(calls) != 1 || calls[0] != wantCalls[0] {
				t.Errorf("provider calls = %+v, want %+v", calls, wantCalls)
			}
			if strings.Contains(logs.String(), pairingSuccessSentinel) {
				t.Errorf("control logs contain pairing sentinel: %q", logs.String())
			}
		})
	}
}

func TestServer_MintPairing_ErrorProjection(t *testing.T) {
	t.Parallel()

	t.Run("provider not configured", func(t *testing.T) {
		var logs bytes.Buffer
		sock, stop := startServerWithPairingProvider(t, nil, slog.New(slog.NewTextHandler(&logs, nil)))
		defer stop()

		resp := channelRoundTrip(t, sock, Request{
			Verb:    VerbPairingMint,
			Pairing: &PairingPayload{DeviceLabel: "phone", AllowRemotePermissions: true},
		})
		assertPairingErrorResponse(t, resp, "pairing.mint: provider not configured")

		got, err := MintPairing(context.Background(), sock, "phone", true)
		if err == nil || err.Error() != "pairing.mint: provider not configured" {
			t.Fatalf("MintPairing error = %v, want fixed not-configured error", err)
		}
		if got != "" {
			t.Errorf("pairing = %q, want empty on error", got)
		}
		assertNoPairingSentinels(t, logs.String(), err.Error())
	})

	t.Run("missing payload", func(t *testing.T) {
		provider := &fakePairingProvider{pairing: pairingSuccessSentinel}
		sock, stop := startServerWithPairingProvider(t, provider.mint, nil)
		defer stop()

		resp := channelRoundTrip(t, sock, Request{Verb: VerbPairingMint})
		assertPairingErrorResponse(t, resp, "pairing.mint: operation failed")
		if calls := provider.recordedCalls(); len(calls) != 0 {
			t.Errorf("provider calls = %+v, want none for missing payload", calls)
		}
	})

	t.Run("provider detail and value are discarded", func(t *testing.T) {
		var logs bytes.Buffer
		provider := &fakePairingProvider{
			pairing: pairingSuccessSentinel,
			err:     errors.New(pairingErrorSentinel),
		}
		sock, stop := startServerWithPairingProvider(t, provider.mint, slog.New(slog.NewTextHandler(&logs, nil)))
		defer stop()

		resp := channelRoundTrip(t, sock, Request{
			Verb:    VerbPairingMint,
			Pairing: &PairingPayload{DeviceLabel: "phone", AllowRemotePermissions: false},
		})
		assertPairingErrorResponse(t, resp, "pairing.mint: operation failed")

		got, err := MintPairing(context.Background(), sock, "phone", false)
		if err == nil || err.Error() != "pairing.mint: operation failed" {
			t.Fatalf("MintPairing error = %v, want fixed operation error", err)
		}
		if got != "" {
			t.Errorf("pairing = %q, want empty on provider error", got)
		}
		assertNoPairingSentinels(t, logs.String(), err.Error())
	})
}

func TestMintPairing_EmptyResponse(t *testing.T) {
	t.Parallel()

	sock := startMisbehavingServer(t, func(conn net.Conn) {
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(Response{})
	})

	got, err := MintPairing(context.Background(), sock, "phone", false)
	if err == nil || err.Error() != "control: empty pairing.mint response" {
		t.Fatalf("MintPairing error = %v, want empty-response error", err)
	}
	if got != "" {
		t.Errorf("pairing = %q, want empty on malformed response", got)
	}
}

func TestMintPairing_SilentPeerUsesControlTimeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	sock := startMisbehavingServer(t, func(conn net.Conn) {
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		<-release
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*DialTimeout)
	defer cancel()
	start := time.Now()
	got, err := MintPairing(ctx, sock, "phone", false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("MintPairing succeeded against silent peer")
	}
	if got != "" {
		t.Errorf("pairing = %q, want empty on timeout", got)
	}
	if ctx.Err() != nil {
		t.Errorf("caller context ended before the control timeout: %v", ctx.Err())
	}
	if elapsed < DialTimeout-time.Second || elapsed > DialTimeout+2*time.Second {
		t.Errorf("elapsed = %v, want operation bounded near %v", elapsed, DialTimeout)
	}
}

func TestMintPairing_EarlierCallerDeadlineOutlastedByProvider(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	var logs bytes.Buffer
	provider := func(string, bool) (string, error) {
		close(entered)
		<-release
		return pairingSuccessSentinel, nil
	}
	sock, stop := startServerWithPairingProvider(t, provider, slog.New(slog.NewTextHandler(&logs, nil)))
	defer func() {
		close(release)
		stop()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	type result struct {
		pairing string
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		pairing, err := MintPairing(ctx, sock, "phone", true)
		resultCh <- result{pairing: pairing, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider was not entered")
	}

	select {
	case got := <-resultCh:
		if got.err == nil {
			t.Fatal("MintPairing succeeded after caller deadline")
		}
		if got.pairing != "" {
			t.Errorf("pairing = %q, want empty after caller deadline", got.pairing)
		}
		assertNoPairingSentinels(t, logs.String(), got.err.Error())
	case <-time.After(2 * time.Second):
		t.Fatal("MintPairing did not honor the earlier caller deadline")
	}
}

func assertPairingErrorResponse(t *testing.T, resp Response, wantError string) {
	t.Helper()
	if resp.Error != wantError {
		t.Errorf("Response.Error = %q, want %q", resp.Error, wantError)
	}
	if resp.Pairing != nil {
		t.Errorf("Response.Pairing = %+v, want nil on error", resp.Pairing)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}
	assertNoPairingSentinels(t, string(encoded))
}

func assertNoPairingSentinels(t *testing.T, values ...string) {
	t.Helper()
	for _, value := range values {
		for _, sentinel := range []string{pairingSuccessSentinel, pairingErrorSentinel} {
			if strings.Contains(value, sentinel) {
				t.Errorf("value %q contains forbidden sentinel %q", value, sentinel)
			}
		}
	}
}
