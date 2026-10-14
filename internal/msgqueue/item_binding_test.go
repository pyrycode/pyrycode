package msgqueue

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestQueueBoundActions(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"drop", "send_now"} {
		t.Run(action, func(t *testing.T) {
			changes, writes := 0, 0
			fail := true
			q, err := New(Config{Deliver: func(context.Context, string, []byte) error { return nil },
				OnChange: func(string) { changes++ },
				SendNow: func(context.Context, string, uint64, []byte) error {
					writes++
					if fail {
						return errors.New("idle")
					}
					return nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			id := q.Enqueue("conv", "first")
			act := q.RemoveItem
			if action == "send_now" {
				act = q.SendNowItem
			}
			if act("conv", id, 100) {
				t.Fatal("unbound entry acted")
			}
			q.BindAcceptedItem("conv", id, 100)
			q.BindAcceptedItem("conv", id, 205) // immutable acceptance
			for _, selector := range []struct {
				c        string
				id, item uint64
			}{
				{"conv", id, 0}, {"conv", id, 205}, {"other", id, 100}, {"conv", id + 1, 100},
			} {
				if act(selector.c, selector.id, selector.item) {
					t.Fatal("wrong selector acted")
				}
			}
			if changes != 1 || writes != 0 || len(q.Snapshot("conv")) != 1 {
				t.Fatal("no-op had side effects")
			}
			if action == "send_now" {
				if act("conv", id, 100) || len(q.Snapshot("conv")) != 1 || changes != 1 {
					t.Fatal("failed write lost binding/entry")
				}
			}
			fail = false
			if !act("conv", id, 100) {
				t.Fatal("matching selector refused")
			}
			next := q.Enqueue("conv", "replacement")
			q.BindAcceptedItem("conv", next, 206)
			before := changes
			if act("conv", id, 100) || act("conv", next, 100) || changes != before {
				t.Fatal("retired selector acted")
			}
			if !q.Remove("conv", next) {
				t.Fatal("legacy removal refused")
			}
		})
	}
}

func TestQueueBindingDuringSendNow(t *testing.T) {
	t.Parallel()
	accepting, bind, writing, failWrite := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var q *Queue
	var err error
	q, err = New(Config{Deliver: func(context.Context, string, []byte) error { return nil },
		OnAccepted: func(c string, m QueuedMessage) {
			close(accepting)
			<-bind
			q.BindAcceptedItem(c, m.ID, 100)
		}, SendNow: func(context.Context, string, uint64, []byte) error {
			close(writing)
			<-failWrite
			return errors.New("idle")
		}})
	if err != nil {
		t.Fatal(err)
	}
	enqueued, sent := make(chan struct{}), make(chan struct{})
	go func() { defer close(enqueued); q.Enqueue("conv", "queued") }()
	<-accepting
	go func() { defer close(sent); q.SendNow("conv", 1) }()
	<-writing
	close(bind)
	<-enqueued
	close(failWrite)
	<-sent
	if !q.RemoveItem("conv", 1, 100) {
		t.Fatal("failed in-flight send lost acceptance binding")
	}
}

func TestQueueBindingDuringAcceptance(t *testing.T) {
	t.Parallel()
	var q *Queue
	var err error
	terminals := 0
	q, err = New(Config{Deliver: func(context.Context, string, []byte) error { return nil },
		OnAccepted: func(c string, m QueuedMessage) {
			q.BindAcceptedItem(c, m.ID, 100)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() { defer wg.Done(); q.RemoveItem(c, m.ID, 100) }()
			}
			wg.Wait()
		}, OnTerminal: func(_ string, _ QueuedMessage, o TerminalOutcome) {
			terminals++
			if o != TerminalRemoved {
				t.Errorf("outcome %s", o)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	q.Enqueue("conv", "queued")
	if len(q.Snapshot("conv")) != 0 || terminals != 1 {
		t.Fatal("acceptance binding unavailable or duplicate terminal outcome")
	}
}
