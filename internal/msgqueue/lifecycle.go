package msgqueue

// AcceptedFunc observes one successful enqueue with its safe message projection.
// It runs synchronously on the enqueue caller, off-lock, and must return promptly.
// It may re-enter queue APIs; a resolution during the callback is deferred until
// it returns. Observers must be safe for concurrent calls across all messages.
type AcceptedFunc func(convID string, msg QueuedMessage)

// TerminalOutcome describes how an accepted message left the memory-only queue.
// A queue ID links to its acceptance only within this conversation and daemon run.
type TerminalOutcome string

const (
	TerminalDelivered TerminalOutcome = "delivered"
	TerminalRemoved   TerminalOutcome = "removed"
	TerminalGiveUp    TerminalOutcome = "give_up"
)

// TerminalFunc observes exactly one resolved outcome per accepted message.
// It runs off-lock, after OnAccepted completes and, for confirmed writes, after
// OnDelivered. It may run on an enqueue/remove/send-now caller or a drain, must
// return promptly, and must be concurrency-safe. Shutdown alone resolves nothing;
// the existing shutdown/confirmed-write observation gap remains.
type TerminalFunc func(convID string, msg QueuedMessage, outcome TerminalOutcome)

// messageLifecycle is shared by copies of a queued message. All mutable fields
// are guarded by q.mu. Once outcome is claimed, outcome and sentNow are immutable.
// No separate registry retains resolved messages.
type messageLifecycle struct {
	accepted bool
	outcome  TerminalOutcome
	sentNow  bool
	attempt  bool
	removed  bool
}

func (q *Queue) accept(convID string, m queued) {
	if q.onAccepted == nil {
		return
	}
	q.onAccepted(convID, m.message(false))
	q.mu.Lock()
	m.lifecycle.accepted = true
	observe := m.lifecycle.outcome != ""
	q.mu.Unlock()
	if observe {
		q.notifyTerminal(convID, m)
	}
}

// resolveLocked claims the terminal outcome once and reports whether its caller
// owns notification. If acceptance is still running, accept owns notification.
// The caller must hold q.mu across both this claim and the resolving mutation.
func (q *Queue) resolveLocked(m queued, outcome TerminalOutcome, sentNow bool) bool {
	if m.lifecycle.outcome != "" {
		return false
	}
	m.lifecycle.outcome = outcome
	m.lifecycle.sentNow = sentNow
	return m.lifecycle.accepted
}

// notifyTerminal is called by the sole notification owner, with q.mu released.
// Each seam gets its own attachment copy, including the legacy delivered seam.
func (q *Queue) notifyTerminal(convID string, m queued) {
	if m.lifecycle.outcome == TerminalDelivered {
		q.notifyDelivered(convID, m, m.lifecycle.sentNow)
	}
	if q.onTerminal != nil {
		q.onTerminal(convID, m.message(m.lifecycle.sentNow), m.lifecycle.outcome)
	}
}
