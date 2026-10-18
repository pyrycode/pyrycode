package main

import "github.com/pyrycode/pyrycode/internal/codexsup"

// codexPermissionConfirmation is current-child authority, never stored intent.
// Both factors refer to one turn/start attempt. All access holds codexRunner.mu.
type codexPermissionConfirmation struct {
	epoch      uint64
	translator *codexsup.Translator
	mode       string
	pending    string
	threadID   string
	responseID string
	startedID  string
}

func (r *codexRunner) invalidateConfirmationLocked() {
	r.confirmation = codexPermissionConfirmation{
		epoch: r.confirmation.epoch + 1, translator: r.confirmation.translator,
	}
}

func (r *codexRunner) confirmationChildLiveLocked() bool {
	if r.client == nil || r.confirmation.translator == nil || r.rotating || r.tearingDown {
		return false
	}
	select {
	case <-r.client.Done():
		return false
	default:
		return true
	}
}

// ConfirmedPermissionMode reports the exact current child's accepted turn
// posture. A successful response AND matching root-thread turn/started are
// required; a settings change alone only describes a future turn.
func (r *codexRunner) ConfirmedPermissionMode() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.confirmationChildLiveLocked() {
		return "", false
	}
	return r.confirmation.mode, r.confirmation.mode != ""
}

// beginPermissionAttempt clears earlier authority before any turn/start I/O.
// The snapshot is rechecked after the queue's commit gate and model resolution.
func (r *codexRunner) beginPermissionAttempt(client *codexsup.Client, generation uint64, mode string) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != client || r.childGeneration != generation || !r.confirmationChildLiveLocked() {
		return 0, false
	}
	r.invalidateConfirmationLocked()
	switch mode {
	case "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions":
		r.confirmation.pending = mode
	}
	r.confirmation.threadID = r.threadID
	return r.confirmation.epoch, true
}

func (r *codexRunner) finishPermissionAttempt(client *codexsup.Client, generation, epoch uint64, turnID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != client || r.childGeneration != generation || r.confirmation.epoch != epoch || !r.confirmationChildLiveLocked() {
		return
	}
	if turnID == "" { // rejection, cancellation or malformed acknowledgement
		r.invalidateConfirmationLocked()
		return
	}
	r.confirmation.responseID = turnID
	r.joinPermissionConfirmationLocked()
}

func (r *codexRunner) permissionTurnStartedLocked(tr *codexsup.Translator, threadID, turnID string) {
	c := &r.confirmation
	if !r.confirmationChildLiveLocked() || c.translator != tr || threadID == "" || threadID != c.threadID || c.pending == "" {
		return
	}
	if c.responseID != "" && c.responseID != turnID {
		r.invalidateConfirmationLocked() // another root turn consumes this authority
		return
	}
	c.startedID = turnID
	r.joinPermissionConfirmationLocked()
}

func (r *codexRunner) joinPermissionConfirmationLocked() {
	c := &r.confirmation
	if c.responseID != "" && c.responseID == c.startedID {
		c.mode = c.pending
	}
}
