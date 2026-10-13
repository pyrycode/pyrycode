package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

var _ HostedAppsAPI = (*apps.Owner)(nil)

func TestHostedAPIRealTransport(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint("blocked_", blocked), func(t *testing.T) {
			p := testHostedRealTransport(t, true)
			body := bytes.Repeat([]byte{0, 255, 1}, 22000)
			p.mgr.cfg.hostedForwardAPI = testAPIPrepare(body, 500)
			init := p.hello([]string{protocol.CapabilityHostedAppsV1})
			_, send, recv, err := init.ReadResp(decodeRespFrame(t, p.next()))
			if err != nil {
				t.Fatal(err)
			}
			request := func(id uint64, typ, payload string) {
				t.Helper()
				msg, err := send.Encrypt([]byte(testAssetEnvelope(id, typ, payload)))
				if err != nil {
					t.Fatal(err)
				}
				p.write(wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, msg))
			}
			if blocked {
				entered, release := p.gate.arm(t)
				request(10, protocol.TypeAppAPIRequest, testAPIPayload([]byte("write")))
				testHostedAwait(t, entered)
				request(11, protocol.TypeAppCancel, fmt.Sprintf(`{"request_id":10,"app_id":%q}`, testAssetApp))
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				for len(p.mgr.ActiveConns(ctx)) != 0 {
					if ctx.Err() != nil {
						t.Fatal("cancellation stalled behind pump")
					}
					time.Sleep(time.Millisecond)
				}
				testResourceIdle(t, p.mgr)
				release()
				testHostedAwait(t, p.socketDone)
				select {
				case e := <-p.wires:
					if len(e.Frame) != 0 {
						t.Fatal("late sealed API output", e)
					}
				default:
				}
				return
			}
			request(10, protocol.TypeAppAPIRequest, testAPIPayload(nil))
			e := decryptAppFrame(t, p.next(), recv)
			var meta protocol.AppResponsePayload
			if e.Type != protocol.TypeAppResponse || e.InReplyTo == nil || *e.InReplyTo != 10 || json.Unmarshal(e.Payload, &meta) != nil || meta.Status != 500 || meta.SizeBytes != uint64(len(body)) || meta.TotalChunks != 3 {
				t.Fatal(e)
			}
			var got []byte
			for i := uint64(0); i < meta.TotalChunks; i++ {
				if i > 0 {
					request(20+i, protocol.TypeAppResponseCredit, testAssetCreditPayload(10, i, testAssetApp))
				}
				raw := threadPlain(t, p.next(), recv)
				if len(raw) > protocol.MaxThreadEnvelopeBytes {
					t.Fatal("plaintext limit")
				}
				var chunk protocol.AppResponseChunkPayload
				if json.Unmarshal(raw, &e) != nil || e.Type != protocol.TypeAppResponseChunk || e.InReplyTo == nil || *e.InReplyTo != 10 || json.Unmarshal(e.Payload, &chunk) != nil || chunk.Index != i || chunk.AppID != testAssetApp {
					t.Fatal("chunk identity", e)
				}
				decoded, err := base64.StdEncoding.Strict().DecodeString(chunk.Data)
				if err != nil || len(decoded) == 0 || len(decoded) > 32768 {
					t.Fatal("chunk bytes", err)
				}
				got = append(got, decoded...)
			}
			if !bytes.Equal(got, body) || meta.SHA256 != fmt.Sprintf("%x", sha256.Sum256(got)) {
				t.Fatal("transport bytes/digest mismatch")
			}
			testResourceIdle(t, p.mgr)
		})
	}
}
