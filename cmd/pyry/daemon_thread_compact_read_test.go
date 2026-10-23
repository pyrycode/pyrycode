package main

import (
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// The phone can receive noise_resp before Run creates its open-session queue.
// Hold that exact boundary so a drain cannot mistake the response for admission.
func TestCompactReadWaitsForOpenSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		b := testControlBindings(t, testConvID, "a")
		w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(c *relay.V2SessionConfig) {
			send := c.Outbound
			c.Outbound = func(e protocol.RoutingEnvelope) error {
				if err := send(e); err != nil {
					return err
				}
				var frame protocol.InnerFrameV2
				if json.Unmarshal(e.Frame, &frame) == nil && frame.Type == protocol.TypeNoiseResp {
					select {
					case <-release:
					case <-t.Context().Done():
					}
				}
				return nil
			}
		})
		t.Cleanup(unblock)
		w.helloCapabilities("phone", []string{protocol.CapabilityInteractive, protocol.CapabilityThread})
		done := make(chan struct{})
		go func() {
			defer close(done)
			if frames := testCompactRead(t, w, mgr, "phone"); len(frames) != 0 {
				t.Errorf("empty drain returned %d application frames", len(frames))
			}
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("drain completed before the session was admitted")
		default:
		}
		unblock()
		<-done
		if err := testDaemonRelayAdmission(t.Context(), mgr, "phone"); err != nil {
			t.Fatal(err)
		}
	})
}
