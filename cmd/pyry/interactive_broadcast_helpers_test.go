package main

import (
	"context"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

func oneInteractiveConn(connID string) *fakeInteractiveBcast {
	return &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: connID, Interactive: true}}},
	}
}

type notifyingBcast struct {
	inner  *fakeInteractiveBcast
	pushed chan struct{}
}

func (n *notifyingBcast) ActiveConns(ctx context.Context) []relay.ActiveConn {
	return n.inner.ActiveConns(ctx)
}

func (n *notifyingBcast) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	err := n.inner.Push(ctx, connID, env)
	select {
	case n.pushed <- struct{}{}:
	default:
	}
	return err
}
