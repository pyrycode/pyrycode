//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func readinessStartHarness(t *testing.T, r *readinessCapture, configure func(string) string) (*perConvHarness, *perConvHarness, func(), func()) {
	t.Helper()
	home := liveHome(t)
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal("isolated workspace unavailable")
	}
	writeStdioPermissionPromptConfig(t, home)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	phonePair, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a", AllowRemotePermissions: true})
	if err != nil {
		t.Fatal("phone pairing unavailable")
	}
	peerPair, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "readiness-peer", AllowRemotePermissions: true})
	if err != nil {
		t.Fatal("peer pairing unavailable")
	}
	public, err := base64.StdEncoding.DecodeString(phonePair.ServerStaticPubkey)
	if err != nil {
		t.Fatal("public identity unavailable")
	}
	seedBootstrapRegistry(t, home, streamModalBootstrapUUID)
	seedBoundConversation(t, home, streamModalConvID, streamModalBootstrapUUID, work)
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal("Claude unavailable")
	}
	claude = configure(claude)
	h := &perConvHarness{home: home, workdir: work}
	peer := &perConvHarness{home: home, workdir: work}
	spawn := func() {
		h.daemon = spawnPermissionDaemon(t, home, work, claude, relayURL, "sonnet", false)
		peer.daemon = h.daemon
		r.daemonPID.Store(int64(h.daemon.cmd.Process.Pid))
		t.Cleanup(func() { h.daemon.stop(t) })
		waitBinaryHello(t, fr, string(phonePair.Server))
	}
	connect := func(client *perConvHarness, token, name string) {
		if client.phone != nil {
			_ = client.phone.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client.phone, err = fakephone.Dial(ctx, fr.URL(), string(phonePair.Server), token, name)
		if err != nil {
			t.Fatal("independent device dial failed")
		}
		client.initSend, client.initRecv = driveHandshakeInteractive(t, client.phone, public, token)
		phone := client.phone
		t.Cleanup(func() { _ = phone.Close() })
	}
	spawn()
	connect(h, phonePair.Token, "phone-a")
	connect(peer, peerPair.Token, "readiness-peer")
	reconnect := func() { connect(h, phonePair.Token, "phone-a") }
	restart := func() {
		_ = h.phone.Close()
		_ = peer.phone.Close()
		h.daemon.stop(t)
		r.Witnesses = append(r.Witnesses, readinessWitness{At: time.Now().UTC(), Boundary: "isolated_daemon_stopped", OK: true})
		spawn()
		connect(peer, peerPair.Token, "readiness-peer")
		reconnect()
		r.Witnesses = append(r.Witnesses, readinessWitness{At: time.Now().UTC(), Boundary: "isolated_daemon_restarted", OK: true})
	}
	r.Setup["independent_peer"] = true
	return h, peer, reconnect, restart
}

func readinessAttachmentChunks(conv, id string, data []byte) []protocol.AttachmentChunkPayload {
	total := (len(data) + 44999) / 45000
	chunks := make([]protocol.AttachmentChunkPayload, 0, total)
	for i := 0; i < total; i++ {
		end := min((i+1)*45000, len(data))
		chunks = append(chunks, protocol.AttachmentChunkPayload{ConversationID: conv, AttachmentID: id, Index: i, TotalChunks: total, Filename: "readiness-document.txt", MimeType: "text/plain", Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Data: data[i*45000 : end]})
	}
	return chunks
}

func (p *readinessReplay) device(peer bool, f func()) {
	if peer {
		p.h, p.peer = p.peer, p.h
		defer func() { p.h, p.peer = p.peer, p.h }()
	}
	f()
}

func (p *readinessReplay) effect(name string, ok bool) {
	p.r.Witnesses = append(p.r.Witnesses, readinessWitness{At: time.Now().UTC(), Boundary: name, Ordinal: p.ordinal, OK: ok})
	if !ok {
		p.t.Fatalf("replay effect failed: %s", name)
	}
}

func (p *readinessReplay) upload(peer, cut bool, data []byte) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		p.t.Fatal("attachment identity unavailable")
	}
	raw[6] = (raw[6] & 15) | 64
	raw[8] = (raw[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	chunks := readinessAttachmentChunks(p.conv, id, data)
	if cut {
		for _, chunk := range chunks[:2] {
			p.send(protocol.TypeAttachmentChunk, chunk)
		}
		p.reconnect()
		p.effect("upload_cut_after_chunk_1_before_final", true)
		messages := 0
		for _, entry := range readinessHistory(p.h.home, p.conv) {
			if entry.Type == "message" {
				messages++
			}
		}
		p.effect("partial_upload_no_message", messages == 0)
	}
	p.device(peer, func() {
		requests := map[uint64]bool{}
		for _, chunk := range chunks {
			p.send(protocol.TypeAttachmentChunk, chunk)
			requests[p.seq] = true
		}
		until := time.Now().Add(30 * time.Second)
		for {
			env := p.receive(until)
			var stored struct {
				ID string `json:"attachment_id"`
			}
			if env.Type == protocol.TypeAttachmentStored && json.Unmarshal(env.Payload, &stored) == nil && stored.ID == id {
				break
			}
			if env.Type == protocol.TypeError && env.InReplyTo != nil && requests[*env.InReplyTo] {
				p.t.Fatal("attachment upload refused")
			}
		}
	})
	p.effect("attachment_stored", true)
	return id
}

func (p *readinessReplay) attachmentTurn(peer bool, ids []string, prompt string) {
	p.device(peer, func() {
		p.send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: p.conv, MessageID: fmt.Sprint(p.seq + 1), Text: prompt, AttachmentIDs: ids})
		p.wait(protocol.TypeTurnEnd, true)
	})
	p.effect("attachment_bearing_turn_completed", true)
}

func (p *readinessReplay) retrieve(peer bool, id string, want []byte) {
	p.device(peer, func() {
		p.send(protocol.TypeRequestAttachment, protocol.RequestAttachmentPayload{ConversationID: p.conv, AttachmentID: id})
		request := p.seq
		chunks := map[int][]byte{}
		total := 0
		until := time.Now().Add(30 * time.Second)
		for time.Now().Before(until) {
			env := p.receive(until)
			if env.InReplyTo == nil || *env.InReplyTo != request {
				continue
			}
			var chunk protocol.AttachmentChunkPayload
			if env.Type != protocol.TypeAttachmentChunk || json.Unmarshal(env.Payload, &chunk) != nil || chunk.AttachmentID != id {
				p.t.Fatal("retrieval refused or mismatched")
			}
			total = chunk.TotalChunks
			chunks[chunk.Index] = chunk.Data
			if total > 0 && len(chunks) == total {
				break
			}
		}
		var got []byte
		for i := 0; i < total; i++ {
			got = append(got, chunks[i]...)
		}
		p.effect("attachment_retrieval_bytes_match", total > 0 && bytes.Equal(got, want))
	})
}

func (p *readinessReplay) attachmentEffect(op string) {
	data := bytes.Repeat([]byte("test-owned document\n"), 5300)
	switch op {
	case "upload-cut":
		id := p.upload(false, true, data)
		p.attachmentTurn(false, []string{id}, "Reply with exactly ping. Do not use tools.")
		p.retrieve(true, id, data)
	case "retrieval-cut":
		id := p.upload(true, false, data)
		p.attachmentTurn(true, []string{id}, "Reply with exactly ping. Do not use tools.")
		_ = p.h.phone.Close()
		p.effect("phone_cut_before_retrieval_request", true)
		p.reconnect()
		p.opening()
		p.retrieve(false, id, data)
	case "share-files", "collision-file":
		id := p.upload(false, false, data)
		ids := []string{id}
		var image []byte
		var imageID string
		if op == "share-files" {
			image, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aG1cAAAAASUVORK5CYII=")
			imageID = p.upload(false, false, image)
			ids = append(ids, imageID)
		}
		p.attachmentTurn(false, ids, "Reply with exactly ping. Do not use tools.")
		p.retrieve(true, id, data)
		if imageID != "" {
			p.retrieve(true, imageID, image)
		}
	case "compact-attachment":
		id := p.upload(false, false, data)
		p.send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: p.conv, MessageID: fmt.Sprint(p.seq + 1), Text: "/compact", AttachmentIDs: []string{id}})
		p.wait("compaction_boundary", true)
		p.effect("compact_with_attachment_completed", true)
	case "offer-file":
		data = []byte("test-owned offered file\n")
		if err := os.WriteFile(filepath.Join(p.h.workdir, "readiness-offer.txt"), data, 0600); err != nil {
			p.t.Fatal("offer fixture unavailable")
		}
		p.turn("Use the pyry_files send_file tool to offer readiness-offer.txt. Then reply done.")
		env := p.wait("attachment_offered", true)
		var offer struct {
			ID string `json:"attachment_id"`
		}
		if json.Unmarshal(env.Payload, &offer) != nil || offer.ID == "" {
			p.t.Fatal("file offer absent")
		}
		p.wait(protocol.TypeTurnEnd, true)
		p.effect("pyry_files_send_file_offered", true)
		p.reconnect()
		p.opening()
		p.retrieve(false, offer.ID, data)
	}
}

func (p *readinessReplay) posts(name string, batch int) {
	accepted := 0
	for i := 0; i < 120; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := control.ChannelPost(ctx, p.h.daemon.socketPath, name, fmt.Sprintf("readiness-post-%d-%03d", batch, i))
		cancel()
		if err != nil {
			p.effect("channel_post_accepted", false)
		}
		accepted++
	}
	p.effect(fmt.Sprintf("channel_posts_%d_120_accepted", batch), accepted == 120)
}

func (p *readinessReplay) offlineEffect() {
	name := fmt.Sprintf("readiness-gap-%d", p.ordinal)
	p.send("rename_conversation", map[string]any{"conversation_id": p.conv, "name": name})
	p.waitReply(p.seq, "conversation_updated")
	p.turn("Reply with exactly ping. Do not use tools.")
	p.wait(protocol.TypeTurnEnd, true)
	p.reconnect()
	p.opening()
	_ = p.h.phone.Close()
	p.effect("phone_offline_peer_retained", true)
	p.posts(name, 1)
	p.device(true, func() {
		until := time.Now().Add(90 * time.Second)
		for {
			env := p.receive(until)
			var delta protocol.AssistantDeltaPayload
			if env.Type == protocol.TypeAssistantDelta && json.Unmarshal(env.Payload, &delta) == nil && delta.ConversationID == p.conv && delta.Text == "readiness-post-1-119" {
				break
			}
		}
		p.effect("older_post_batch_delivery_fenced", true)
	})
	// Posts produce turn_end too; persistently fence the ordinary peer turn by its message identity.
	p.device(true, func() {
		p.turn("Reply with exactly ping. Do not use tools.")
		message := fmt.Sprint(p.seq)
		until := time.Now().Add(90 * time.Second)
		for time.Now().Before(until) {
			p.receive(until)
			var turn readinessTurn
			readinessFoldTurn(&turn, readinessHistory(p.h.home, p.conv), p.conv, message, "Reply with exactly ping. Do not use tools.")
			delivered := !turn.Delivered.IsZero()
			completed := false
			for _, e := range readinessHistory(p.h.home, p.conv) {
				if delivered && e.Type == "turn_end" && e.TS.After(turn.Delivered) {
					completed = true
				}
			}
			if completed {
				p.effect("non_channel_peer_turn_completed", true)
				return
			}
		}
		p.effect("non_channel_peer_turn_completed", false)
	})
	p.posts(name, 2)
	p.observePosts("before_restart")
	p.restart()
	p.epoch++
	p.effect("restart_epoch_advanced", true)
	p.opening()
	p.send("request_history", map[string]any{"conversation_id": p.conv, "limit": 50, "cursor": ""})
	p.waitReply(p.seq, "history_page")
	p.effect("post_restart_history_page", true)
	p.observePosts("after_restart")
}

func TestReadinessAttachmentChunks(t *testing.T) {
	data := bytes.Repeat([]byte("test-owned document\n"), 5300)
	chunks := readinessAttachmentChunks("conversation", "attachment", data)
	if len(chunks) != 3 || len(chunks[0].Data) != 45000 || len(chunks[1].Data) != 45000 {
		t.Fatalf("cut needs three bounded chunks; got %d", len(chunks))
	}
	var rebuilt []byte
	for i, chunk := range chunks {
		if chunk.Index != i || chunk.TotalChunks != len(chunks) || chunk.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
			t.Fatal("whole-file metadata mismatch")
		}
		rebuilt = append(rebuilt, chunk.Data...)
	}
	if !bytes.Equal(rebuilt, data) {
		t.Fatal("attachment bytes changed")
	}
}

func (p *readinessReplay) observePosts(phase string) {
	counts := map[int]int{}
	for _, entry := range readinessHistory(p.h.home, p.conv) {
		if entry.Type != "assistant_delta" {
			continue
		}
		var delta protocol.AssistantDeltaPayload
		if json.Unmarshal(entry.Payload, &delta) != nil {
			continue
		}
		for _, batch := range []int{1, 2} {
			if strings.HasPrefix(delta.Text, fmt.Sprintf("readiness-post-%d-", batch)) {
				counts[batch]++
			}
		}
	}
	if p.row.Effects == nil {
		p.row.Effects = map[string]int{}
	}
	for _, batch := range []int{1, 2} {
		p.row.Effects[fmt.Sprintf("batch_%d_delivered_%s", batch, phase)] = counts[batch]
	}
}
