//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type readinessReplayRow struct {
	Method, Owner, Retention, Edges, FidelityLimit string
	Operations                                     []string
	Started, Finished                              time.Time
	Completed                                      bool
	EpochBefore, EpochAfter                        int
	Counts                                         map[string]int
	FrameCounts                                    map[string]int
	ErrorCodes                                     map[string]int
	Effects                                        map[string]int
}

func readinessJavaHash(s string) int32 {
	var h int32
	for _, c := range s {
		h = 31*h + int32(c)
	}
	return h
}

func readinessPrefix() []readinessReplayRow {
	return []readinessReplayRow{
		{Method: "interactiveTurn_pingPrompt_footerShowsContextUsage", Owner: "main", Operations: []string{"create", "ping"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_backgroundAgent_followsBottomUntilFinished", Owner: "main", Operations: []string{"create", "agent"}, Retention: "retained; idle eviction disabled as assumption", Edges: "background/held work starts before subsequent foreground/control operations", FidelityLimit: "Agent progress tool-count and UI scrolling omitted; fixture hold/release preserved"},
		{Method: "interactiveTurn_finishedReply_systemCopyCopiesSelectedWord", Owner: "main", Operations: []string{"create", "ping"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "selection text replaced with short reply; UI copy omitted"},
		{Method: "interactiveTurn_peerQueue_staysConsistentAcrossClients", Owner: "main", Operations: []string{"create", "queue"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_twoHostsArchive_staysPerHost", Owner: "main", Operations: []string{"create", "archive", "unarchive", "delete"}, Retention: "removed", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_deleteConversation_removesFromListAndClosesThread", Owner: "main", Operations: []string{"create", "delete"}, Retention: "removed", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_twoHostsCollidingConversationId_stayPerHost", Owner: "main", Operations: []string{"collision", "rename", "reconnect"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "second-host operations excluded; main collision uses seeded bootstrap conversation"},
		{Method: "interactiveTurn_secondHostRenameAndUnpair_leavesFirstHostUntouched", Owner: "main", Operations: []string{"list"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "second-host rename/unpair excluded; main list read only"},
		{Method: "interactiveTurn_offlineRead_reconcilesPeerTurnOnReconnect", Owner: "main", Operations: []string{"create", "promote", "offline-effects"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_stopBackgroundTask_completesThenRepliesAgain", Owner: "main", Operations: []string{"create", "stop-task", "ping"}, Retention: "retained; idle eviction disabled as assumption", Edges: "background/held work starts before subsequent foreground/control operations", FidelityLimit: "task stop fixture uses local held HTTP request rather than mobile bridge"},
		{Method: "interactiveTurn_claudeOfferedFile_opensAndSavesAfterRestart", Owner: "main", Operations: []string{"create", "offer-file"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_muteChannel_roundTripsThroughTheHost", Owner: "main", Operations: []string{"create", "promote", "mute", "unmute", "delete"}, Retention: "removed", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_reopenAfterReconnect_footerShowsContextUsageBeforeAnyTurn", Owner: "main", Operations: []string{"create", "ping", "reconnect"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_permissionHeldTool_statusAreaNamesRunningTool", Owner: "main", Operations: []string{"create", "held-tool"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_diagnosticBundles_stayOnTheirOwningHosts", Owner: "main", Operations: []string{"create", "delete"}, Retention: "removed", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "diagnostic archive transfer omitted; marker create/delete retained"},
		{Method: "interactiveTurn_sessionError_recoversDroppedAndRetainedBacklog", Owner: "session-error", Operations: []string{}, Retention: "retained; idle eviction disabled as assumption", Edges: "dedicated daemon effects excluded from main load", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_toolPrompt_rendersToolStepInThread", Owner: "main", Operations: []string{"create", "tool"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "source tool-completion edge approximated by tool_result; no idle barrier"},
		{Method: "interactiveTurn_compactWithAttachment_compactsAndClearsTheStrip", Owner: "main", Operations: []string{"create", "ping", "compact-attachment"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_permissionPrompts_heldPerConversation", Owner: "answer", Operations: []string{}, Retention: "retained; idle eviction disabled as assumption", Edges: "dedicated daemon effects excluded from main load", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_pingPrompt_statusSheetShowsRunningModel", Owner: "main", Operations: []string{"create", "ping", "opening"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_createEditArchiveChannel_readsPromptBack", Owner: "main", Operations: []string{"create", "delete", "create", "promote", "prompt", "ping", "prompt", "reset", "ping", "clear-prompt", "archive", "unarchive", "delete"}, Retention: "removed", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "existing-channel archive fixture and exact system prompts omitted; new-channel mutation/reset edges retained"},
		{Method: "interactiveTurn_modelChange_roundTripsAndStaysPerConversation", Owner: "main", Operations: []string{"create", "create", "settings", "ping", "settings", "opening"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "dynamic vocabulary picks replaced with allowlisted sonnet/haiku variants; first chat has no turn"},
		{Method: "interactiveTurn_reconnect_footerReadingsAndModelChangeSurvive", Owner: "main", Operations: []string{"create", "ping", "reconnect", "settings", "ping", "settings"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "dynamic model pick replaced with allowlisted haiku; settings restored to sonnet"},
		{Method: "interactiveTurn_interruptedUpload_retriesIntoOneMessageWithItsBytes", Owner: "main", Operations: []string{"create", "upload-cut"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_sendQueuedNow_reachesRunningTurn", Owner: "main", Operations: []string{"create", "ping", "send-now"}, Retention: "retained; idle eviction disabled as assumption", Edges: "background/held work starts before subsequent foreground/control operations", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_inheritedEffort_footerShowsAppliedValueAfterTurn", Owner: "main", Operations: []string{"create", "ping"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_chosenEffort_appliesFromTheFirstTurn", Owner: "main", Operations: []string{"create", "effort", "ping", "opening"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "dynamic model/effort selection replaced with sonnet/medium"},
		{Method: "interactiveTurn_directShareShortcut_arrivesAtPeerWithItsBytes", Owner: "main", Operations: []string{"create", "create", "share-files"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_questionAnswer_reachesTheAskingConversation", Owner: "answer", Operations: []string{}, Retention: "retained; idle eviction disabled as assumption", Edges: "dedicated daemon effects excluded from main load", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_markdownLink_opensLiveNoteInReader", Owner: "main", Operations: []string{"create", "note", "note"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "note file commands retained; reader refresh traffic omitted"},
		{Method: "interactiveTurn_permissionAnswer_reachesOnlyTheAskingConversation", Owner: "answer", Operations: []string{}, Retention: "retained; idle eviction disabled as assumption", Edges: "dedicated daemon effects excluded from main load", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_attentionDot_followsARealTurn", Owner: "answer", Operations: []string{}, Retention: "retained; idle eviction disabled as assumption", Edges: "dedicated daemon effects excluded from main load", FidelityLimit: "UI spacing and original outcome unknown; source-derived candidate only"},
		{Method: "interactiveTurn_collidingConversationId_phoneFileStaysOnItsHost", Owner: "main", Operations: []string{"collision", "collision-file"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
		{Method: "interactiveTurn_newSession_rendersSessionBoundaryDelimiter", Owner: "main", Operations: []string{"create", "ping", "reset"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "reset wrap-up retained; no post-reset ping absent from source"},
		{Method: "interactiveTurn_backgroundTask_countsInActionsMenuAndPanel", Owner: "main", Operations: []string{"create", "background"}, Retention: "retained; idle eviction disabled as assumption", Edges: "background/held work starts before subsequent foreground/control operations", FidelityLimit: "source background sleep role represented by local held fixture released by test"},
		{Method: "interactiveTurn_scannerConfirm_waitsForHostThenOpensList", Owner: "main", Operations: []string{"list"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "second-host scanner/pair handshake excluded; main list read only"},
		{Method: "interactiveTurn_interruptedRetrieval_retryLoadsThePeersFile", Owner: "main", Operations: []string{"create", "retrieval-cut"}, Retention: "retained; idle eviction disabled as assumption", Edges: "source terminal frame only; no added idle barrier", FidelityLimit: "actual peer/attachment/restart effects; generated test bytes; original pacing/outcomes unknown; Android assertions remain owned by mobile"},
	}
}

func readinessOpeningTypes() []string {
	return []string{"mcp_status_request", "request_context_usage", "request_session_settings", "request_history"}
}

type readinessReplay struct {
	t           *testing.T
	h           *perConvHarness
	r           *readinessCapture
	subjects    map[string]int
	reconnect   func()
	seq         uint64
	conv        string
	ordinal     int
	nextOrdinal int
	peer        *perConvHarness
	restart     func()
	epoch       int
	hold        *httptest.Server
	release     func()
	requested   chan struct{}
	row         *readinessReplayRow
	peerDevice  bool
	method      string
	requests    map[uint64]int
}

func (p *readinessReplay) send(kind string, payload any) {
	p.seq++
	p.recordSend(kind, p.seq)
	sealEnvelope(p.t, p.h.phone, p.h.initSend, protocol.Envelope{ID: p.seq, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(p.t, payload)})
}

func (p *readinessReplay) opening() {
	for _, kind := range readinessOpeningTypes() {
		payload := map[string]any{"conversation_id": p.conv}
		if kind == "request_history" {
			payload["cursor"], payload["limit"] = "", 50
		}
		p.send(kind, payload)
		p.r.Witnesses = append(p.r.Witnesses, readinessWitness{At: time.Now().UTC(), Boundary: "phone_" + kind, Ordinal: p.ordinal, OK: true})
	}
}

func (p *readinessReplay) bind(conv, session string) int {
	ordinal, known := p.subjects[conv]
	if !known {
		p.nextOrdinal++
		ordinal = p.nextOrdinal
	}
	p.subjects[conv], p.subjects[session] = ordinal, ordinal
	p.conv, p.ordinal = conv, ordinal
	return ordinal
}

func (p *readinessReplay) create() {
	p.conv = p.createRequest()
	reg, err := conversations.Load(filepath.Join(p.h.home, ".pyry", "test", "conversations.json"))
	if err != nil {
		p.t.Fatal("replay binding unavailable")
	}
	c, ok := reg.Get(conversations.ConversationID(p.conv))
	if !ok || c.CurrentSessionID == "" {
		p.t.Fatal("replay session missing")
	}
	p.bind(p.conv, c.CurrentSessionID)
	p.opening()
}

func (p *readinessReplay) turn(prompt string) {
	p.send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: p.conv, MessageID: strconv.FormatUint(p.seq+1, 10), Text: prompt})
}

func (p *readinessReplay) receive(until time.Time) protocol.Envelope {
	raw, err := p.h.phone.ReceiveBytes(time.Until(until))
	if err != nil {
		p.r.Setup["prefix_phone_read_failed"] = true
		p.t.Fatal("bounded replay phone read failed")
	}
	var frame protocol.InnerFrameV2
	if json.Unmarshal(raw, &frame) != nil {
		p.t.Fatal("invalid replay frame")
	}
	if frame.Type != protocol.TypeNoiseMsg {
		return protocol.Envelope{}
	}
	cipher, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		p.t.Fatal("invalid replay ciphertext")
	}
	plain, err := p.h.initRecv.Decrypt(cipher)
	if err != nil {
		p.t.Fatal("replay nonce desynchronized")
	}
	var env protocol.Envelope
	if json.Unmarshal(plain, &env) != nil {
		p.t.Fatal("invalid replay envelope")
	}
	p.recordReceived(env)
	if p.row != nil {
		if p.row.FrameCounts == nil {
			p.row.FrameCounts = map[string]int{}
			p.row.ErrorCodes = map[string]int{}
		}
		p.row.FrameCounts[env.Type]++
		if env.Type == protocol.TypeError {
			var e struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(env.Payload, &e) == nil && len(e.Code) < 64 {
				p.row.ErrorCodes[e.Code]++
			}
		}
	}
	if env.Type == protocol.TypeError {
		n, _ := p.r.Setup["prefix_error_frames"].(int)
		p.r.Setup["prefix_error_frames"] = n + 1
	}
	return env
}

func (p *readinessReplay) waitReply(id uint64, kind string) {
	until := time.Now().Add(30 * time.Second)
	for time.Now().Before(until) {
		env := p.receive(until)
		if env.InReplyTo == nil || *env.InReplyTo != id {
			continue
		}
		if env.Type != kind {
			p.t.Fatalf("replay control refused; wanted %s", kind)
		}
		return
	}
	p.t.Fatalf("replay control reply missing: %s", kind)
}

func (p *readinessReplay) allow(env protocol.Envelope) {
	var m protocol.ModalShownPayload
	if json.Unmarshal(env.Payload, &m) != nil || m.ModalID == "" || m.ConversationID != p.conv {
		p.t.Fatal("replay modal scope invalid")
	}
	p.send(protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: m.ModalID, OptionID: "allow_once", AnswerToken: fmt.Sprintf("readiness-%d", p.seq+1)})
}

func (p *readinessReplay) wait(kind string, autoAllow bool) protocol.Envelope {
	until := time.Now().Add(120 * time.Second)
	for time.Now().Before(until) {
		env := p.receive(until)
		var scope struct {
			ConversationID string `json:"conversation_id"`
		}
		if json.Unmarshal(env.Payload, &scope) != nil || scope.ConversationID != p.conv {
			continue
		}
		if env.Type == kind {
			return env
		}
		if env.Type == protocol.TypeModalShown && autoAllow {
			p.allow(env)
		}
	}
	p.t.Fatalf("replay did not reach %s", kind)
	return protocol.Envelope{}
}

func (p *readinessReplay) queueID(message string) uint64 {
	until := time.Now().Add(30 * time.Second)
	for time.Now().Before(until) {
		env := p.receive(until)
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var q struct {
			ConversationID string `json:"conversation_id"`
			Queued         []struct {
				MessageID string `json:"message_id"`
				ID        uint64 `json:"queued_msg_id"`
			} `json:"queued"`
		}
		if json.Unmarshal(env.Payload, &q) != nil || q.ConversationID != p.conv {
			continue
		}
		for _, item := range q.Queued {
			if item.MessageID == message {
				return item.ID
			}
		}
	}
	p.t.Fatal("replay queue item missing")
	return 0
}

func (p *readinessReplay) heldFixture() string {
	if p.hold != nil {
		p.release()
		p.hold.Close()
	}
	released := make(chan struct{})
	p.requested = make(chan struct{})
	requested := p.requested
	var requestOnce sync.Once
	var once sync.Once
	p.release = func() { once.Do(func() { close(released) }) }
	p.hold = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/release" {
			p.release()
			_, _ = w.Write([]byte("released"))
			return
		}
		requestOnce.Do(func() { close(requested) })
		select {
		case <-released:
			_, _ = w.Write([]byte("finished"))
		case <-r.Context().Done():
		}
	}))
	server, release := p.hold, p.release
	p.t.Cleanup(func() { release(); server.Close() })
	return p.hold.URL
}

func (p *readinessReplay) awaitHeldRequest() {
	select {
	case <-p.requested:
	case <-time.After(30 * time.Second):
		p.t.Fatal("held fixture never received the tool request")
	}
}

func (p *readinessReplay) settings(model, effort string) {
	reg, err := conversations.Load(filepath.Join(p.h.home, ".pyry", "test", "conversations.json"))
	if err != nil {
		p.t.Fatal("settings binding unavailable")
	}
	c, ok := reg.Get(conversations.ConversationID(p.conv))
	if !ok {
		p.t.Fatal("settings conversation missing")
	}
	p.send("set_session_settings", map[string]any{"session_id": c.CurrentSessionID, "model": model, "effort": effort})
}

func (p *readinessReplay) operation(op string) {
	switch op {
	case "upload-cut", "retrieval-cut", "share-files", "collision-file", "compact-attachment", "offer-file":
		p.attachmentEffect(op)
	case "offline-effects":
		p.offlineEffect()
	case "create":
		p.create()
	case "list":
		p.send("list_conversations", map[string]any{})
	case "collision":
		p.conv, p.ordinal = streamModalConvID, 0
		p.opening()
	case "opening":
		p.opening()
	case "reconnect":
		p.reconnect()
		p.opening()
	case "ping":
		p.turn("Reply with exactly ping. Do not use tools.")
		p.wait(protocol.TypeTurnEnd, true)
	case "tool":
		p.turn("Run exactly this Bash command in the foreground: python3 -c 'print(950)'. Then reply done.")
		p.wait(protocol.TypeToolResult, true)
	case "held-tool":
		url := p.heldFixture()
		p.turn("Run exactly this foreground Bash command: curl --max-time 100 --silent --show-error " + url + "/hold . Wait for it, then reply done.")
		m := p.wait(protocol.TypeModalShown, false)
		p.allow(m)
		p.awaitHeldRequest()
		p.release()
		p.wait(protocol.TypeTurnEnd, true)
	case "agent":
		url := p.heldFixture()
		p.turn("Use the Agent tool to start one background agent (run_in_background=true). Tell it to run four Bash commands python3 -c 'print(1)' and then curl --max-time 100 --silent --show-error " + url + "/hold . It must wait for that command, then reply finished. You must return immediately after starting it.")
		p.wait("background_task_started", true)
		p.turn("Reply newer. Do not use tools.")
		p.wait(protocol.TypeTurnEnd, true)
		p.release()
		p.turn("Reply later. Do not use tools.")
		p.wait(protocol.TypeTurnEnd, true)
	case "stop-task", "background":
		url := p.heldFixture()
		p.turn("Run this Bash command with run_in_background=true: curl --max-time 100 --silent --show-error " + url + "/hold . Return immediately, do not wait for it.")
		env := p.wait("background_task_started", true)
		var task struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(env.Payload, &task) != nil || task.TaskID == "" {
			p.t.Fatal("background identity missing")
		}
		if op == "stop-task" {
			p.send("stop_background_task", map[string]any{"conversation_id": p.conv, "task_id": task.TaskID})
		} else {
			p.release()
		}
		p.wait("background_task_updated", true)
		p.release()
	case "queue":
		p.device(true, func() { p.turn("Run exactly this foreground Bash command: python3 -c 'print(950)'. Then reply done.") })
		var m protocol.Envelope
		p.device(true, func() { m = p.wait(protocol.TypeModalShown, false) })
		p.turn("Reply with exactly ping. Do not use tools.")
		p.turn("This queued message will be removed.")
		id := p.queueID(strconv.FormatUint(p.seq, 10))
		p.send("dequeue_message", map[string]any{"conversation_id": p.conv, "queued_msg_id": id})
		p.device(true, func() { p.turn("This peer queued message will also be removed.") })
		id = p.queueID(strconv.FormatUint(p.seq, 10))
		p.device(true, func() {
			p.send("dequeue_message", map[string]any{"conversation_id": p.conv, "queued_msg_id": id})
			p.allow(m)
		})
		p.wait(protocol.TypeTurnEnd, true)
		p.wait(protocol.TypeTurnEnd, true)
	case "send-now":
		url := p.heldFixture()
		p.device(true, func() {
			p.turn("Run exactly this foreground Bash command: curl --max-time 100 --silent --show-error " + url + "/hold . Wait for it, then include any marker from new input in your final reply.")
			m := p.wait(protocol.TypeModalShown, false)
			p.allow(m)
		})
		p.awaitHeldRequest()
		p.turn("Include marker readiness in your final reply.")
		id := p.queueID(strconv.FormatUint(p.seq, 10))
		p.send("send_queued_now", map[string]any{"conversation_id": p.conv, "queued_msg_id": id})
		p.release()
		p.wait(protocol.TypeTurnEnd, true)
	case "reset":
		p.send("new_session", map[string]any{"conversation_id": p.conv})
		env := p.wait("session_transition", true)
		var changed struct {
			NewSessionID string `json:"new_session_id"`
		}
		if json.Unmarshal(env.Payload, &changed) == nil && changed.NewSessionID != "" {
			p.subjects[changed.NewSessionID] = p.ordinal
		}
		p.opening()
	case "compact":
		p.turn("/compact")
		p.wait("compaction_boundary", true)
	case "note":
		p.turn("Use Bash to write exactly note into readiness-note.txt using printf note > readiness-note.txt . Then reply done.")
		p.wait(protocol.TypeTurnEnd, true)
	case "settings":
		p.settings("haiku", "")
	case "effort":
		p.settings("sonnet", "medium")
	case "prompt", "clear-prompt":
		var value any = "Answer briefly."
		if op == "clear-prompt" {
			value = nil
		}
		p.send("set_system_prompt", map[string]any{"conversation_id": p.conv, "system_prompt": value})
	case "mute", "unmute":
		p.send("set_conversation_muted", map[string]any{"conversation_id": p.conv, "muted": op == "mute"})
	default:
		kind := map[string]string{"archive": "archive_conversation", "unarchive": "unarchive_conversation", "delete": "delete_conversation", "promote": "promote_conversation", "rename": "rename_conversation"}[op]
		if kind == "" {
			p.t.Fatal("unknown replay operation")
		}
		payload := map[string]any{"conversation_id": p.conv}
		if op == "promote" || op == "rename" {
			payload["name"] = fmt.Sprintf("readiness-%d-%d", p.ordinal, p.seq)
			if op == "promote" {
				payload["cwd"] = p.h.workdir
			}
		}
		p.send(kind, payload)
		want := "conversation_updated"
		if op == "delete" {
			want = "conversation_deleted"
		}
		p.waitReply(p.seq, want)
	}
}

func readinessStartupFixture(t *testing.T, r *readinessCapture) func(string) string {
	dependency := ""
	if dependent, _ := r.Setup["startup_dependency_variant"].(bool); dependent {
		dependency = readinessDependencyService(t, r)
	}
	return func(bin string) string {
		home := os.Getenv("HOME")
		work := filepath.Join(home, "work")
		script := filepath.Join(work, "readiness-startup.py")
		source := `import json,sys,time,pathlib,urllib.request,os,urllib.parse
dependency=DEPENDENCY_LITERAL
log=pathlib.Path(__file__).with_name("readiness-startup.jsonl")
child=os.environ.get("PYRY_E2E_READINESS_CHILD", "")
def note(role,phase):
 with log.open("a") as f: f.write(json.dumps({"role":role,"phase":phase,"unix":time.time(),"child":child})+"\n")
def await_service(role):
 note(role,"dependent_entry")
 if dependency:
  try:
   with urllib.request.urlopen(dependency+"/"+role+"?child="+urllib.parse.quote(child),timeout=9) as reply:
    if reply.status!=200: raise RuntimeError("fixture not ready")
  except Exception:
   note(role,"dependency_timeout")
   raise
 note(role,"dependency_completed")
if len(sys.argv)>1:
 await_service("SessionStart")
 note("SessionStart","completed")
 sys.exit(0)
note("MCP","started")
for line in sys.stdin:
 try: msg=json.loads(line)
 except ValueError: continue
 if "id" not in msg: continue
 method=msg.get("method")
 if method=="initialize":
  await_service("MCP")
  note("MCP","initialized")
  result={"protocolVersion":msg.get("params",{}).get("protocolVersion","2024-11-05"),"capabilities":{"tools":{}},"serverInfo":{"name":"readiness-fixture","version":"1"}}
 elif method=="tools/list": result={"tools":[]}
 else: result={}
 print(json.dumps({"jsonrpc":"2.0","id":msg["id"],"result":result}),flush=True)
`
		source = strings.Replace(source, "DEPENDENCY_LITERAL", strconv.Quote(dependency), 1)
		if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
			t.Fatal("startup fixture unavailable")
		}
		mcp := map[string]any{"mcpServers": map[string]any{"readiness-fixture": map[string]any{"command": "python3", "args": []string{script}}}}
		b, _ := json.Marshal(mcp)
		if os.WriteFile(filepath.Join(work, ".mcp.json"), b, 0o600) != nil {
			t.Fatal("MCP fixture unavailable")
		}
		dir := filepath.Join(home, ".claude")
		if os.MkdirAll(dir, 0o700) != nil {
			t.Fatal("hook directory unavailable")
		}
		hook := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "python3 '" + script + "' hook", "timeout": 10}}}}}}
		b, _ = json.Marshal(hook)
		if os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600) != nil {
			t.Fatal("hook fixture unavailable")
		}
		t.Setenv("PYRY_E2E_READINESS_MCP_CONFIG", filepath.Join(work, ".mcp.json"))
		r.Setup["startup_fixture_source_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
		r.Setup["startup_fixture"] = map[string]any{"provenance": "test-owned hypothesis; original operator configuration unknown", "mcp_role": "stdio initialize/tools-list, no tools", "hook_role": "SessionStart scalar timestamp then successful exit", "startup_dependencies": "immediate control or independent per-child service health check; sequential roles supported; client timeout 9 seconds; expected dependency waiting is not a host defect", "mcp_launch_provenance": "explicit test-owned --mcp-config; workspace auto-discovery was suppressed by daemon strict config in prior batches", "hook_timeout_seconds": 10, "workspace": "isolated HOME/work; historical HOME was daemon workdir", "source": "pinned mobile launcher reads operator configuration; resolved original MCP/hook roles unknown"}

		t.Cleanup(func() {
			b, err := os.ReadFile(filepath.Join(work, "readiness-startup.jsonl"))
			if err != nil {
				r.Setup["startup_observations_known"] = false
				return
			}
			observations := []map[string]any{}
			for _, line := range bytes.Split(b, []byte{'\n'}) {
				var o struct {
					Role, Phase, Child string
					Unix               float64
				}
				if json.Unmarshal(line, &o) != nil || (o.Role != "MCP" && o.Role != "SessionStart") {
					continue
				}
				r.Startup = append(r.Startup, readinessStartupEvent{Role: o.Role, Phase: o.Phase, At: time.Unix(0, int64(o.Unix*1e9)).UTC(), ChildKey: o.Child})
			}
			r.Setup["startup_observations_known"], r.Setup["startup_observations"] = true, observations
		})
		return bin
	}
}

func (p *readinessReplay) recordSend(kind string, id uint64) {
	if p.requests == nil {
		p.requests = map[uint64]int{}
	}
	p.requests[id] = len(p.requests) + 1
	device := "phone"
	if p.peerDevice {
		device = "peer"
	}
	p.r.Requests = append(p.r.Requests, readinessRequestEvent{At: time.Now().UTC(), Phase: "send", Type: kind, Device: device, Method: p.method, RequestOrdinal: p.requests[id], ConversationOrdinal: p.ordinal, RestartEpoch: p.epoch})
}

func (p *readinessReplay) recordReceived(env protocol.Envelope) {
	device := "phone"
	if p.peerDevice {
		device = "peer"
	}
	ordinal := 0
	if env.InReplyTo != nil {
		ordinal = p.requests[*env.InReplyTo]
	}
	phase := "arrival"
	if env.Type == protocol.TypeError {
		phase = "refusal_arrival"
	}
	var scope struct {
		ConversationID string `json:"conversation_id"`
	}
	_ = json.Unmarshal(env.Payload, &scope)
	p.r.Requests = append(p.r.Requests, readinessRequestEvent{At: time.Now().UTC(), Phase: phase, Type: env.Type, Device: device, Method: p.method, ReplyToOrdinal: ordinal, ConversationOrdinal: p.subjects[scope.ConversationID], RestartEpoch: p.epoch})
}

func (p *readinessReplay) createRequest() string {
	p.send(protocol.TypeCreateConversation, protocol.CreateConversationPayload{})
	request := p.seq
	until := time.Now().Add(30 * time.Second)
	for time.Now().Before(until) {
		env := p.receive(until)
		if env.InReplyTo == nil || *env.InReplyTo != request {
			continue
		}
		if env.Type != protocol.TypeConversationCreated {
			p.t.Fatal("create conversation refused")
		}
		var result protocol.ConversationCreatedPayload
		if json.Unmarshal(env.Payload, &result) != nil || result.ID == "" {
			p.t.Fatal("create conversation identity missing")
		}
		return result.ID
	}
	p.t.Fatal("create conversation reply missing")
	return ""
}
