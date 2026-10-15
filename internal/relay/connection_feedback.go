package relay

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/pyrycode/pyrycode/internal/transport"
)

const wakePendingCap = 128
const wakeLifetime = 60 * time.Second

type wakeBinding struct {
	generation       *transport.Generation
	ctx              context.Context
	context          string
	expires          time.Time
	result           chan string
	written, replied bool
	stops            []func() bool
}

// SendPushWakeFeedback submits on the current socket and waits for write completion.
// requestContext is an immutable caller-owned correlation handle or snapshot; it
// stays local and is never logged or serialized. The returned buffered channel
// emits it at most once for invalid_token, then closes. A closed channel without
// a value means retirement, expiry or feedback overflow, never invalidity.
// Cancellation retires feedback even after this method returns successfully.
func (c *Connection) SendPushWakeFeedback(ctx context.Context, platform, token, requestContext string) (<-chan string, error) {
	if c.client == nil {
		return nil, transport.ErrNotConnected
	}
	g := c.client.Generation()
	if g == nil {
		return nil, transport.ErrNotConnected
	}
	c.wakeMu.Lock()
	now := c.wakeTime()
	for id, b := range c.wakes {
		if !c.wakeLive(b, now) {
			c.retireWake(id, b)
		}
	}
	result := make(chan string, 1)
	id := ""
	var b *wakeBinding
	if len(c.wakes) < wakePendingCap {
		if c.wakeSeq == ^uint64(0) {
			c.wakeMu.Unlock()
			return nil, errors.New("relay: wake request identifiers exhausted")
		}
		c.wakeSeq++
		id = "wake-" + strconv.FormatUint(c.wakeSeq, 36)
		b = &wakeBinding{generation: g, ctx: ctx, context: requestContext, expires: now.Add(wakeLifetime), result: result}
		if c.wakes == nil {
			c.wakes = make(map[string]*wakeBinding)
		}
		c.wakes[id] = b
		retire := func() {
			c.wakeMu.Lock()
			defer c.wakeMu.Unlock()
			if c.wakes[id] == b {
				c.retireWake(id, b)
			}
		}
		b.stops = append(b.stops, context.AfterFunc(ctx, retire), context.AfterFunc(g.Context(), retire))
		if c.wakeAfter != nil {
			stop := c.wakeAfter(wakeLifetime, retire)
			b.stops = append(b.stops, func() bool { stop(); return true })
		} else {
			b.stops = append(b.stops, time.AfterFunc(wakeLifetime, retire).Stop)
		}
	} else {
		close(result)
	}
	c.wakeMu.Unlock()
	raw, err := json.Marshal(pushWakeEnvelope{PushWake: pushWakeRequest{Platform: platform, Token: token, RequestID: id}})
	if err == nil {
		err = c.client.SendGeneration(ctx, g, raw)
	} else {
		err = errMarshalPushWake
	}
	c.wakeMu.Lock()
	defer c.wakeMu.Unlock()
	if b != nil && c.wakes[id] == b {
		if err != nil || !c.wakeLive(b, c.wakeTime()) {
			c.retireWake(id, b)
		} else {
			b.written = true
			if b.replied {
				c.notifyWake(id, b)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Connection) wakeTime() time.Time {
	if c.wakeNow != nil {
		return c.wakeNow()
	}
	return time.Now()
}

func (c *Connection) wakeLive(b *wakeBinding, now time.Time) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	return b.ctx.Err() == nil && b.generation.Context().Err() == nil && now.Before(b.expires)
}

// retireWake holds wakeMu. Stopping callbacks never waits for a running callback.
func (c *Connection) retireWake(id string, b *wakeBinding) {
	delete(c.wakes, id)
	for _, stop := range b.stops {
		stop()
	}
	close(b.result)
}

func (c *Connection) notifyWake(id string, b *wakeBinding) {
	b.result <- b.context
	c.retireWake(id, b)
}

func validWakeID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := range id {
		ch := id[i]
		if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func (c *Connection) consumeWakeResult(g *transport.Generation, raw json.RawMessage) {
	var result struct {
		RequestID *string `json:"request_id"`
		Code      *string `json:"code"`
	}
	if json.Unmarshal(raw, &result) != nil || result.RequestID == nil || result.Code == nil || !validWakeID(*result.RequestID) || *result.Code != "invalid_token" {
		return
	}
	c.wakeMu.Lock()
	defer c.wakeMu.Unlock()
	id := *result.RequestID
	b := c.wakes[id]
	if b == nil || b.generation != g {
		return
	}
	if !c.wakeLive(b, c.wakeTime()) {
		c.retireWake(id, b)
		return
	}
	b.replied = true
	if b.written {
		c.notifyWake(id, b)
	}
}
