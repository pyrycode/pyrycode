package e2emcp

import (
	"errors"
	"io"
	"sync"
)

// RPCEvent stores the complete original line as a string: encoding/json's
// RawMessage marshaler would compact it. Decoding this string recovers every
// literal byte. Credentials/environment must never be placed in this ledger.
type RPCEvent struct {
	Sequence          int
	Connection        string
	Direction         Direction
	Raw               string
	NativeInputCount  int
	UserOrdinal       int
	NativeStdoutCount int
}

// Coordinator orders successful complete native and MCP forwards under one
// mutex. A caller must perform the actual pipe write inside the supplied
// function; a pre-write observer cannot establish these causal barriers.
// Validation is supplied by the owner, including privacy and exact profile
// bindings. This primitive does not authorize processes or perform tool work.
type Coordinator struct {
	mu                  sync.Mutex
	failed, closed      bool
	input, user, stdout int
	bytes               int
	events              []RPCEvent
	sessions            map[string]*RPCSession
	validate            func(Direction, []byte) error
}

func NewCoordinator(validate func(Direction, []byte) error) (*Coordinator, error) {
	if validate == nil {
		return nil, protocolError()
	}
	return &Coordinator{sessions: map[string]*RPCSession{}, validate: validate}, nil
}

// Register is called only after the runtime has authenticated a current native
// child. A recorded connection name alone is never launch authority.
func (c *Coordinator) Register(connection string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || c.closed || connection == "" || len(connection) > 128 || c.sessions[connection] != nil || len(c.sessions) >= 8 {
		return c.reject()
	}
	c.sessions[connection] = NewRPCSession()
	return nil
}
func (c *Coordinator) reject() error {
	c.failed = true
	return errors.New("MCP coordinated forwarding failed")
}

// NativeInput advances the all-input count after every full current forward,
// including controls. User frames separately advance the user ordinal. The caller validates its original frame.
func (c *Coordinator) NativeInput(user bool, forward func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || c.closed || forward == nil {
		return c.reject()
	}
	if err := forward(); err != nil {
		return c.reject()
	}
	c.input++
	if user {
		c.user++
	}
	return nil
}
func (c *Coordinator) NativeOutput(forward func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || c.closed || forward == nil {
		return c.reject()
	}
	if err := forward(); err != nil {
		return c.reject()
	}
	c.stdout++
	return nil
}

// ForwardRPC validates a trial ledger before writing. Failed/partial writes
// poison the coordinator, so they can never become a successful event or seal.
// wire excludes exactly one newline separator; forward must write it unchanged
// and append that separator, returning io.ErrShortWrite for any short write.
func (c *Coordinator) ForwardRPC(connection string, direction Direction, wire []byte, forward func([]byte) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	session := c.sessions[connection]
	if c.failed || c.closed || session == nil || forward == nil || len(c.events) >= MaxRPCFrames || len(wire) > MaxFrameBytes-c.bytes || c.validate(direction, wire) != nil {
		return c.reject()
	}
	trial := cloneSession(session)
	if _, err := trial.Observe(direction, wire); err != nil {
		return c.reject()
	}
	owned := append([]byte(nil), wire...)
	if err := forward(append([]byte(nil), owned...)); err != nil {
		return c.reject()
	}
	c.bytes += len(owned)
	c.sessions[connection] = trial
	c.events = append(c.events, RPCEvent{Sequence: len(c.events) + 1, Connection: connection, Direction: direction, Raw: string(owned), NativeInputCount: c.input, UserOrdinal: c.user, NativeStdoutCount: c.stdout})
	return nil
}

// CloseRPC requires both directions to have reached complete, non-partial EOF
// and the runtime's actual server Wait to have completed successfully. The
// boolean facts are supplied only by that owner; this is no process proof API.
func (c *Coordinator) CloseRPC(connection string, inputEOF, outputEOF bool, waitErr error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	session := c.sessions[connection]
	if c.failed || c.closed || session == nil || !inputEOF || !outputEOF || waitErr != nil {
		return c.reject()
	}
	if _, err := session.Finish(); err != nil {
		return c.reject()
	}
	return nil
}
func (c *Coordinator) Finish() ([]RPCEvent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || c.closed || len(c.sessions) == 0 {
		return nil, c.reject()
	}
	for _, session := range c.sessions {
		if !session.closed || session.failed {
			return nil, c.reject()
		}
	}
	c.closed = true
	return append([]RPCEvent(nil), c.events...), nil
}
func cloneSession(s *RPCSession) *RPCSession {
	out := *s
	out.seen = map[string]bool{}
	out.pending = map[string]RPCFrame{}
	out.ordinals = map[string]int{}
	for k, v := range s.seen {
		out.seen[k] = v
	}
	for k, v := range s.pending {
		out.pending[k] = cloneRPCFrame(v)
	}
	for k, v := range s.ordinals {
		out.ordinals[k] = v
	}
	out.exchanges = make([]Exchange, len(s.exchanges))
	for i, v := range s.exchanges {
		out.exchanges[i] = Exchange{Ordinal: v.Ordinal, Request: cloneRPCFrame(v.Request), Response: cloneRPCFrame(v.Response)}
	}
	return &out
}

// WriteLine preserves the raw JSON exactly and requires a complete separator.
func WriteLine(w io.Writer, raw []byte) error {
	line := append(append([]byte(nil), raw...), '\n')
	n, err := w.Write(line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return nil
}

// Abort prevents a failed runtime transfer or child from leaving sealable evidence.
func (c *Coordinator) Abort() { c.mu.Lock(); defer c.mu.Unlock(); c.failed = true }
