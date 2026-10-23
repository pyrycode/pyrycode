// Package fakephone is an in-process WebSocket client that speaks the
// phone half of the mobile↔relay protocol (docs/protocol-mobile.md
// § Authentication, § Message envelope). It exists so daemon-side e2e
// tests can script the appendix flow as if a phone were connected,
// without depending on a real device or platform stack.
//
// The package lives under internal/e2e/internal/ to visibility-fence it
// from non-e2e callers. It is the sibling of fakerelay: the relay owns
// the routing-envelope wrap/unwrap, so this client speaks raw
// protocol.Envelope JSON.
//
// # Surface
//
// Client provides synchronous socket I/O and retains decrypted observations
// consumed during setup. Consumers own reconnect, Noise negotiation and
// application-envelope sequencing. ErrReceiveTimeout and ErrClosed distinguish
// receive deadlines from explicit client shutdown.
//
// # Concurrency
//
//   - One goroutine may call Send and one (possibly the same) may call
//     Receive; coder/websocket permits one concurrent reader plus one
//     concurrent writer.
//   - Close is safe to call from any goroutine and unblocks an in-flight
//     Receive with ErrClosed.
package fakephone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// maxFrameBytes mirrors fakerelay and the production transport client.
const maxFrameBytes = 1 << 20

// Sentinel errors. Both are matchable via errors.Is.
var (
	ErrReceiveTimeout = errors.New("fakephone: receive timeout")
	ErrClosed         = errors.New("fakephone: client closed")
)

// Client is a WS client wired to the fake-relay's /v1/client endpoint.
// Construct with Dial; tear down with Close.
type Client struct {
	conn *websocket.Conn

	readMu  sync.Mutex
	pending chan readResult

	mu              sync.Mutex
	closed          bool
	lastCloseStatus websocket.StatusCode
	lastCloseSet    bool
	deferred        []protocol.Envelope
}

type readResult struct {
	data []byte
	err  error
}

// DeferEnvelopes restores already decrypted observations ahead of unread wire
// frames. Callers must retain their original order; no Noise nonce is replayed.
func (c *Client) DeferEnvelopes(envs []protocol.Envelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deferred = append(append([]protocol.Envelope(nil), envs...), c.deferred...)
}

// TakeDeferred returns the oldest saved observation, if any.
func (c *Client) TakeDeferred() (protocol.Envelope, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.deferred) == 0 {
		return protocol.Envelope{}, false
	}
	e := c.deferred[0]
	c.deferred[0] = protocol.Envelope{}
	c.deferred = c.deferred[1:]
	return e, true
}

// Dial opens /v1/client at baseURL with the three required headers set
// verbatim from the arguments. baseURL is the bare ws://host:port form
// (e.g. as returned by fakerelay.Server.URL()); Dial appends the path.
func Dial(ctx context.Context, baseURL, serverID, token, deviceName string) (*Client, error) {
	hdr := http.Header{}
	hdr.Set("x-pyrycode-server", serverID)
	hdr.Set("x-pyrycode-token", token)
	hdr.Set("x-pyrycode-device-name", deviceName)

	conn, _, err := websocket.Dial(ctx, baseURL+"/v1/client", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("fakephone dial: %w", err)
	}
	conn.SetReadLimit(maxFrameBytes)
	return &Client{conn: conn}, nil
}

// Send marshals env and writes it as a single WS text frame.
func (c *Client) Send(env protocol.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("fakephone marshal: %w", err)
	}
	return c.SendBytes(data)
}

// SendBytes writes data as a single WS text frame without any marshalling.
// Used by v2 e2e tests where the wire shape inside RoutingEnvelope.Frame is
// an InnerFrameV2 rather than a protocol.Envelope.
func (c *Client) SendBytes(data []byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.mu.Unlock()

	if err := c.conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return ErrClosed
		}
		return fmt.Errorf("fakephone write: %w", err)
	}
	return nil
}

// Receive reads one WS text frame and unmarshals it into a
// protocol.Envelope. On deadline expiry returns ErrReceiveTimeout;
// unless inheriting an outstanding PollBytes read. Otherwise coder/websocket
// closes the underlying connection when the
// Read context is canceled, so a timed-out Client cannot be reused.
// After Close, returns ErrClosed.
func (c *Client) Receive(timeout time.Duration) (protocol.Envelope, error) {
	data, err := c.ReceiveBytes(timeout)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return protocol.Envelope{}, fmt.Errorf("fakephone unmarshal: %w", err)
	}
	return env, nil
}

// ReceiveBytes reads one WS text frame and returns the raw payload. Same
// timeout / close semantics as Receive. Used by v2 e2e tests where the
// wire shape inside RoutingEnvelope.Frame is an InnerFrameV2 rather than
// a protocol.Envelope.
func (c *Client) ReceiveBytes(timeout time.Duration) ([]byte, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.pending != nil {
		return c.pollLocked(timeout)
	}
	return c.readBytes(timeout)
}

// PollBytes retains one read across short observation deadlines and successive
// consumers. The timeout bounds this observation, without canceling the socket.
// Close joins the read before returning. Call readers serially.
func (c *Client) PollBytes(timeout time.Duration) ([]byte, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	return c.pollLocked(timeout)
}

func (c *Client) pollLocked(timeout time.Duration) ([]byte, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	if c.pending == nil {
		c.pending = make(chan readResult, 1)
		ch := c.pending
		go func() {
			data, err := c.readBytes(30 * time.Minute)
			ch <- readResult{data, err}
		}()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-c.pending:
		c.pending = nil
		return r.data, r.err
	case <-timer.C:
		return nil, ErrReceiveTimeout
	}
}

func (c *Client) readBytes(timeout time.Duration) ([]byte, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, data, err := c.conn.Read(ctx)
	if err != nil {
		// Capture peer-side close status (e.g. 4401 for auth.invalid_token)
		// before unwinding so tests can assert on the close code via
		// LastCloseStatus. websocket.CloseStatus returns -1 when err is
		// not a CloseError; only record real close codes.
		if code := websocket.CloseStatus(err); code != -1 {
			c.mu.Lock()
			c.lastCloseStatus = code
			c.lastCloseSet = true
			c.mu.Unlock()
		}
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return nil, ErrClosed
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
			return nil, ErrReceiveTimeout
		}
		return nil, fmt.Errorf("fakephone read: %w", err)
	}
	return data, nil
}

// LastCloseStatus returns the WS close status code observed by the most
// recent Receive call that failed with a CloseError. ok is false when no
// such close has been observed yet (e.g. the conn is still open, or was
// closed locally via Close()).
func (c *Client) LastCloseStatus() (websocket.StatusCode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCloseStatus, c.lastCloseSet
}

// Close shuts down the WS connection cleanly. Idempotent. After Close,
// every Send and Receive returns ErrClosed.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.conn.Close(websocket.StatusNormalClosure, "phone closing")
	c.readMu.Lock()
	if c.pending != nil {
		<-c.pending
		c.pending = nil
	}
	c.readMu.Unlock()
	return nil
}
