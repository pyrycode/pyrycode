package e2emcp

import (
	"context"
	"time"
)

type replaySchedule struct {
	ctx         context.Context
	events      []RPCEvent
	next        int
	connections map[string]bool
	match       func(RPCEvent, []byte) error
	stopWatch   func() bool
}

// NewReplayCoordinator checks the complete immutable source trace, then gates
// actual current forwards against its causal counters. Matching a response is
// delegated to the profile's full literal or exact typed binding validator; this
// primitive never supplies an MCP response, approval verdict or file copy.
func NewReplayCoordinator(ctx context.Context, events []RPCEvent, validate func(Direction, []byte) error, match func(RPCEvent, []byte) error) (*Coordinator, error) {
	if ctx == nil || ctx.Err() != nil || match == nil || len(events) == 0 || len(events) > MaxRPCFrames {
		return nil, protocolError()
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) || time.Until(deadline) > 20*time.Minute {
		return nil, protocolError()
	}
	c, err := NewCoordinator(validate)
	if err != nil {
		return nil, err
	}
	sessions := map[string]*RPCSession{}
	connections := map[string]bool{}
	total := 0
	input, user, stdout := 0, 0, 0
	for i, event := range events {
		total += len(event.Raw)
		if event.Sequence != i+1 || event.Connection == "" || len(event.Connection) > 128 || total > MaxFrameBytes || event.NativeInputCount < input || event.UserOrdinal < user || event.UserOrdinal > event.NativeInputCount || event.NativeStdoutCount < stdout {
			return nil, protocolError()
		}
		session := sessions[event.Connection]
		if session == nil {
			if len(sessions) >= 8 {
				return nil, protocolError()
			}
			session = NewRPCSession()
			sessions[event.Connection] = session
			connections[event.Connection] = true
		}
		if validate(event.Direction, []byte(event.Raw)) != nil {
			return nil, protocolError()
		}
		if _, err := session.Observe(event.Direction, []byte(event.Raw)); err != nil {
			return nil, err
		}
		input, user, stdout = event.NativeInputCount, event.UserOrdinal, event.NativeStdoutCount
	}
	for _, session := range sessions {
		if _, err := session.Finish(); err != nil {
			return nil, err
		}
	}
	c.replay = &replaySchedule{ctx: ctx, events: append([]RPCEvent(nil), events...), connections: connections, match: match}
	c.replay.stopWatch = context.AfterFunc(ctx, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.closed {
			c.failed = true
			c.notify()
		}
	})
	return c, nil
}
func (c *Coordinator) notify() {
	if c.changed != nil {
		close(c.changed)
	}
	c.changed = make(chan struct{})
}
func (c *Coordinator) changedOrCancelled() error {
	changed := c.changed
	ctx := c.replay.ctx
	c.mu.Unlock()
	select {
	case <-changed:
	case <-ctx.Done():
	}
	c.mu.Lock()
	if c.failed || c.closed || c.checkpointed || ctx.Err() != nil {
		return c.reject()
	}
	return nil
}
func (c *Coordinator) beforeNative(input bool) error {
	if c.replay == nil {
		return nil
	}
	if c.replay.ctx.Err() != nil {
		return c.reject()
	}
	for c.replay.next < len(c.replay.events) {
		event := c.replay.events[c.replay.next]
		if event.NativeInputCount < c.input || event.NativeStdoutCount < c.stdout || event.UserOrdinal < c.user {
			return c.reject()
		}
		atBoundary := event.NativeStdoutCount == c.stdout
		if input {
			atBoundary = event.NativeInputCount == c.input
		}
		if !atBoundary {
			return nil
		}
		if err := c.changedOrCancelled(); err != nil {
			return err
		}
	}
	return nil
}
func (c *Coordinator) beforeRPC(connection string, direction Direction) error {
	if c.replay == nil {
		return nil
	}
	if c.replay.ctx.Err() != nil {
		return c.reject()
	}
	for {
		if c.replay.next >= len(c.replay.events) {
			return c.reject()
		}
		event := c.replay.events[c.replay.next]
		if event.NativeInputCount < c.input || event.NativeStdoutCount < c.stdout || event.UserOrdinal < c.user {
			return c.reject()
		}
		if event.Connection == connection && event.Direction == direction && event.NativeInputCount == c.input && event.NativeStdoutCount == c.stdout && event.UserOrdinal == c.user {
			return nil
		}
		if err := c.changedOrCancelled(); err != nil {
			return err
		}
	}
}
