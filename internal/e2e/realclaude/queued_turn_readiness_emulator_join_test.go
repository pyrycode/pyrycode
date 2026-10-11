//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type readinessPrivateState struct {
	At                                  time.Time
	Owner, Epoch, Conversation, Session string
	Process                             int
	Kind                                string
	Entry                               *history.Entry
	Settings                            *readinessEffectiveSettings
}

type readinessEmulatorJoins struct {
	states                                                               []readinessPrivateState
	owners, epochs, conversations, sessions, children, requests, devices map[string]int
	bindings                                                             map[string]string
	observations                                                         []readinessPrivateObservation
}

func readinessNewJoins(observations []readinessPrivateObservation, states []readinessPrivateState) (*readinessEmulatorJoins, error) {
	j := &readinessEmulatorJoins{owners: map[string]int{}, epochs: map[string]int{}, conversations: map[string]int{}, sessions: map[string]int{}, children: map[string]int{}, requests: map[string]int{}, devices: map[string]int{}, bindings: map[string]string{}, observations: observations, states: states}
	for _, o := range observations {
		if o.Owner == "" || o.Epoch == "" || o.Process <= 0 || o.At.IsZero() {
			return nil, fmt.Errorf("unowned host observation")
		}
		readinessOrdinal(j.owners, o.Owner)
		key := o.Owner + "/" + o.Epoch
		if _, ok := j.epochs[key]; !ok {
			n := 0
			for k := range j.epochs {
				if strings.HasPrefix(k, o.Owner+"/") {
					n++
				}
			}
			j.epochs[key] = n
		}
		if o.Child != "" {
			readinessOrdinal(j.children, key+"/"+o.Child)
		}
	}
	for _, s := range states {
		if j.owners[s.Owner] == 0 {
			return nil, fmt.Errorf("unowned persisted state")
		}
		if _, ok := j.epochs[s.Owner+"/"+s.Epoch]; !ok {
			return nil, fmt.Errorf("unwitnessed persistence epoch")
		}
		if s.Conversation != "" {
			readinessOrdinal(j.conversations, s.Owner+"/"+s.Conversation)
		}
		session := s.Session
		if s.Entry != nil && s.Entry.Session != nil {
			session = s.Entry.Session.SessionID
		}
		if session != "" {
			key := s.Owner + "/" + s.Epoch + "/" + session
			readinessOrdinal(j.sessions, s.Owner+"/"+session)
			if old, ok := j.bindings[key]; ok && old != s.Conversation {
				j.bindings[key] = ""
			} else if !ok {
				j.bindings[key] = s.Conversation
			}
		}
	}
	return j, nil
}

func readinessOrdinal(m map[string]int, key string) int {
	if key == "" {
		return 0
	}
	if m[key] == 0 {
		m[key] = len(m) + 1
	}
	return m[key]
}

func (j *readinessEmulatorJoins) fold(r *readinessCapture) error {
	for _, o := range j.observations {
		epoch := j.epochs[o.Owner+"/"+o.Epoch]
		owner := j.owners[o.Owner]
		child := j.children[o.Owner+"/"+o.Epoch+"/"+o.Child]
		if o.Boundary != "" {
			switch o.Boundary {
			case "activate_begin", "activate_end", "idle_begin", "idle_end", "turn_write", "child_started", "child_wait_complete", "posture_armed", "permission_write", "stdout_forward_begin", "stdout_forward_end", "ack_forwarder_line", "ack_stdout_line", "ack_processing", "gate_released":
			default:
				return fmt.Errorf("unknown boundary category")
			}
			conv, session := o.Subject, ""
			switch o.Boundary {
			case "activate_begin", "activate_end", "idle_begin", "idle_end", "turn_write":
				session = j.sessionAt(o.Owner, o.Epoch, conv, o.At)
				for _, w := range j.observations {
					if w.Owner == o.Owner && w.Epoch == o.Epoch && w.Subject == session && w.Boundary == "child_started" && !w.At.After(o.At) {
						child = j.children[w.Owner+"/"+w.Epoch+"/"+w.Child]
					}
				}
			default:
				session = o.Subject
				conv = j.bindings[o.Owner+"/"+o.Epoch+"/"+session]
			}
			r.Witnesses = append(r.Witnesses, readinessWitness{At: o.At, Boundary: o.Boundary, Ordinal: j.conversations[o.Owner+"/"+conv], SessionOrdinal: j.sessions[o.Owner+"/"+session], OwnerOrdinal: owner, ChildOrdinal: child, RestartEpoch: epoch, OK: o.OK})
			continue
		}
		if o.Phase != "host_received" && o.Phase != "outbound_queued" {
			return fmt.Errorf("unknown request phase")
		}
		if o.Kind != "unknown" && protocol.IsKnownAppType(protocol.Envelope{Type: o.Kind}) != nil {
			return fmt.Errorf("unknown request category")
		}
		device := readinessOrdinal(j.devices, o.Owner+"/"+o.Epoch+"/"+o.Device)
		key := o.Owner + "/" + o.Epoch + "/" + o.Device + "/"
		ordinal := 0
		if o.Phase == "host_received" && o.Request != 0 {
			ordinal = readinessOrdinal(j.requests, key+strconv.FormatUint(o.Request, 10))
		}
		reply := j.requests[key+strconv.FormatUint(o.ReplyTo, 10)]
		outcome := o.Outcome
		if outcome != "unknown" && outcome != "refused" && outcome != "correlated_reply" && outcome != "accepted" {
			return fmt.Errorf("unsafe request outcome")
		}
		r.Requests = append(r.Requests, readinessRequestEvent{At: o.At, Phase: o.Phase, Type: o.Kind, Device: fmt.Sprintf("connection-%d", device), Method: "unknown; no witnessed request-method join", RequestOrdinal: ordinal, ReplyToOrdinal: reply, ConversationOrdinal: j.conversations[o.Owner+"/"+o.Subject], RestartEpoch: epoch, OwnerOrdinal: owner, Outcome: outcome})
	}
	return nil
}

func (j *readinessEmulatorJoins) targets(states []readinessPrivateState, prompt string, methods []readinessMethodInterval) ([]readinessEmulatorTarget, error) {
	if prompt == "" {
		return nil, fmt.Errorf("private target comparison input missing")
	}
	var interval readinessMethodInterval
	for _, m := range methods {
		if m.Name == "interactiveTurn_pingPrompt_streamsPingReplyIntoThread" {
			interval = m
		}
	}
	if interval.Started.IsZero() {
		return nil, nil
	}
	var targets []readinessEmulatorTarget
	for _, s := range states {
		if s.Entry == nil || s.Entry.Type != "send_accepted" {
			continue
		}
		var accepted struct {
			ConversationID, MessageID, Text string
			AcceptedAt                      time.Time `json:"accepted_at"`
		}
		// Message fields use the persisted wire names, independent from Go field names.
		var fields struct {
			ConversationID string    `json:"conversation_id"`
			MessageID      string    `json:"message_id"`
			Text           string    `json:"text"`
			AcceptedAt     time.Time `json:"accepted_at"`
		}
		if json.Unmarshal(s.Entry.Payload, &fields) != nil {
			return nil, fmt.Errorf("persisted acceptance malformed")
		}
		accepted.ConversationID, accepted.MessageID, accepted.Text, accepted.AcceptedAt = fields.ConversationID, fields.MessageID, fields.Text, fields.AcceptedAt
		if accepted.Text != prompt || accepted.AcceptedAt.Before(interval.Started) || !interval.Ended.IsZero() && accepted.AcceptedAt.After(interval.Ended) {
			continue
		}
		if accepted.ConversationID != s.Conversation || accepted.MessageID == "" || accepted.AcceptedAt.IsZero() {
			return nil, fmt.Errorf("target acceptance identity invalid")
		}
		t := readinessEmulatorTarget{OwnerOrdinal: j.owners[s.Owner], RestartEpoch: j.epochs[s.Owner+"/"+s.Epoch], ConversationOrdinal: j.conversations[s.Owner+"/"+s.Conversation], Enqueued: accepted.AcceptedAt}
		var session, child, reply string
		terminal := false
		var messageID uint64
		messages, deliveries, openings := 0, 0, 0
		entries := []readinessPrivateState{}
		for _, e := range states {
			if e.Entry != nil && e.Owner == s.Owner && e.Conversation == s.Conversation {
				entries = append(entries, e)
			}
		}
		sort.Slice(entries, func(a, b int) bool { return entries[a].Entry.ID < entries[b].Entry.ID })
		for _, e := range entries {
			h := e.Entry
			if h.ID <= s.Entry.ID {
				continue
			}
			var payload struct {
				MessageID       string `json:"message_id"`
				Role            string `json:"role"`
				Text            string `json:"text"`
				AcceptedEntryID uint64 `json:"accepted_entry_id"`
				DeliveryEntryID uint64 `json:"delivery_entry_id"`
			}
			if json.Unmarshal(h.Payload, &payload) != nil {
				continue
			}
			switch h.Type {
			case "message":
				if payload.MessageID == accepted.MessageID && payload.Role == "user" {
					messages++
					messageID = h.ID
					t.PromptMatches = readinessBool(payload.Text == prompt)
					if h.Session != nil {
						session = h.Session.SessionID
					}
				}
			case "send_delivered":
				if payload.AcceptedEntryID == s.Entry.ID && messageID > 0 && payload.DeliveryEntryID == messageID {
					deliveries++
					t.Delivered = h.TS
					t.RestartEpoch = j.epochs[e.Owner+"/"+e.Epoch]
				}
			case "main_turn_opened":
				if messageID > 0 {
					openings++
					if openings == 1 {
						t.Opened = h.TS
					}
				}
			case "turn_end":
				if openings == 1 {
					terminal = true
				}
			case "assistant_delta":
				if openings == 1 {
					reply += payload.Text
				}
			}
		}
		if !t.Opened.IsZero() {
			if terminal {
				t.ReplyMatches = readinessBool(strings.EqualFold(strings.TrimSpace(reply), "ping"))
			}
			t.ExactlyOnce = readinessBool(messages == 1 && deliveries == 1 && openings == 1)
		}
		t.SessionOrdinal = j.sessions[s.Owner+"/"+session]
		for _, o := range j.observations {
			if o.Owner == s.Owner && j.epochs[o.Owner+"/"+o.Epoch] == t.RestartEpoch && o.Subject == session && !t.Delivered.IsZero() && !o.At.After(t.Delivered) {
				if o.Boundary == "child_started" && o.OK {
					child = o.Child
					t.Confirmed = time.Time{}
				}
				if o.Boundary == "gate_released" && o.OK && o.Child == child && child != "" {
					t.Confirmed = o.At
				}
			}
		}
		t.ChildOrdinal = j.children[s.Owner+"/"+readinessEpochKey(j, s.Owner, t.RestartEpoch)+"/"+child]
		if !t.Delivered.IsZero() && child != "" {
			t.CurrentChild = readinessBool(!t.Confirmed.IsZero())
		}
		if !t.Delivered.IsZero() && !t.Opened.IsZero() {
			t.Within90Seconds = readinessBool(t.Delivered.Sub(t.Enqueued) <= 90*time.Second && t.Opened.Sub(t.Enqueued) <= 90*time.Second)
		}
		targets = append(targets, t)
	}
	if len(targets) > 1 {
		return nil, fmt.Errorf("persisted named target missing or ambiguous")
	}
	return targets, nil
}

func readinessEpochKey(j *readinessEmulatorJoins, owner string, epoch int) string {
	for key, n := range j.epochs {
		if n == epoch && strings.HasPrefix(key, owner+"/") {
			return strings.TrimPrefix(key, owner+"/")
		}
	}
	return ""
}
func readinessBool(b bool) *bool { return &b }

func readinessPrivateRows[T any](b []byte) ([]T, error) {
	var rows []T
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r T
		if json.Unmarshal(line, &r) != nil {
			return nil, fmt.Errorf("private evidence malformed")
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func (j *readinessEmulatorJoins) sessionAt(owner, epoch, conv string, at time.Time) string {
	session := ""
	var witnessed time.Time
	for _, s := range j.states {
		if s.Owner != owner || s.Epoch != epoch || s.Conversation != conv {
			continue
		}
		when, id := s.At, s.Session
		if s.Entry != nil {
			when = s.Entry.TS
			if s.Entry.Session != nil {
				id = s.Entry.Session.SessionID
			}
		}
		if id != "" && !when.IsZero() && !when.After(at) && (witnessed.IsZero() || when.After(witnessed)) {
			witnessed, session = when, id
		}
	}
	return session
}
