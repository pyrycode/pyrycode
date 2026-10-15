package devices

import (
	"sync"
	"testing"
)

func TestClearPushRegistrationIfMatch(t *testing.T) {
	for _, mismatch := range []string{"none", "identity", "platform", "token", "empty"} {
		t.Run(mismatch, func(t *testing.T) {
			d := Device{TokenHash: "hash", Name: "phone", Platform: "fcm", PushToken: "T", StaticKey: "key", AllowRemotePermissions: true}
			r := &Registry{devices: []Device{d}}
			hash, platform, token := d.TokenHash, d.Platform, d.PushToken
			switch mismatch {
			case "identity":
				hash = "other"
			case "platform":
				platform = "apns"
			case "token":
				token = "R"
			case "empty":
				r.devices[0].Platform, r.devices[0].PushToken = "", ""
				platform, token = "", ""
				d = r.devices[0]
			}
			if got := r.ClearPushRegistrationIfMatch(hash, platform, token); got != (mismatch == "none") {
				t.Fatal("incorrect conditional mutation")
			}
			if mismatch == "none" {
				d.Platform, d.PushToken = "", ""
			}
			if r.List()[0] != d {
				t.Fatal("unrelated state changed")
			}
		})
	}
	for range 100 {
		r := &Registry{devices: []Device{{TokenHash: "hash", Platform: "fcm", PushToken: "T"}}}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); r.ClearPushRegistrationIfMatch("hash", "fcm", "T") }()
		go func() { defer wg.Done(); r.UpdatePushRegistration("hash", "fcm", "R", "renamed") }()
		wg.Wait()
		if r.List()[0].PushToken != "R" {
			t.Fatal("replacement erased")
		}
	}
}
