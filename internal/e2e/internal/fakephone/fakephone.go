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
// One Client type, four methods (Dial, Send, Receive, Close) and two
// sentinel errors (ErrReceiveTimeout, ErrClosed). The harness is
// synchronous: each method blocks on its own I/O. No reconnect, no
// ping pump, no hello/hello_ack handling — those are envelope-sequencing
// concerns owned by the consumer test.
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

	mu     sync.Mutex
	closed bool
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
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.mu.Unlock()

	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("fakephone marshal: %w", err)
	}
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
// note that coder/websocket closes the underlying connection when the
// Read context is canceled, so a timed-out Client cannot be reused.
// After Close, returns ErrClosed.
func (c *Client) Receive(timeout time.Duration) (protocol.Envelope, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return protocol.Envelope{}, ErrClosed
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, data, err := c.conn.Read(ctx)
	if err != nil {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return protocol.Envelope{}, ErrClosed
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
			return protocol.Envelope{}, ErrReceiveTimeout
		}
		return protocol.Envelope{}, fmt.Errorf("fakephone read: %w", err)
	}

	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return protocol.Envelope{}, fmt.Errorf("fakephone unmarshal: %w", err)
	}
	return env, nil
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
	return nil
}
