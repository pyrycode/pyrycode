package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// Observe actual filesystem operations only around the installed callback;
// the list handler's separate legacy watermark scan is outside this contract.
func TestDaemonThreadLastShownIO(t *testing.T) {
	for _, count := range []int{1, 128} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			cfg, h, store, shadow, path, _ := testThreadWatermark(t, false)
			for i := 1; i < count; i++ {
				if _, err := h.Append(testConvID, "unsupported", []byte(`{}`), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Load(t.Context(), testConvID); err != nil {
				t.Fatal(err)
			}
			testShadowWait(t, func() bool { return store.Status(testConvID).Version == uint64(count) })
			if err := store.Unload(testConvID); err != nil {
				t.Fatal(err)
			}
			fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Close(fd)
			// Close events separate sequential opens of the same file so inotify
			// cannot coalesce them into one event before the observer reads them.
			if _, err := syscall.InotifyAddWatch(fd, filepath.Dir(path), syscall.IN_OPEN|syscall.IN_CLOSE_NOWRITE|syscall.IN_CREATE|syscall.IN_DELETE|syscall.IN_MOVED_FROM|syscall.IN_MOVED_TO|syscall.IN_MODIFY|syscall.IN_ATTRIB); err != nil {
				t.Fatal(err)
			}
			observe := func(want int) (int, error) {
				var events [8192]byte
				n, err := syscall.Read(fd, events[:])
				if err == syscall.EAGAIN {
					n, err = 0, nil
				}
				if err != nil {
					return 0, err
				}
				opens := 0
				for offset := 0; offset < n; {
					mask := binary.NativeEndian.Uint32(events[offset+4:])
					length := int(binary.NativeEndian.Uint32(events[offset+12:]))
					name := string(events[offset+16 : offset+16+length])
					if (mask != syscall.IN_OPEN && mask != syscall.IN_CLOSE_NOWRITE) || strings.TrimRight(name, "\x00") != "thread-certificate.json" {
						return 0, fmt.Errorf("unexpected filesystem operation mask=%x name=%q", mask, name)
					}
					if mask == syscall.IN_OPEN {
						opens++
					}
					offset += 16 + length
				}
				if opens != want {
					return opens, fmt.Errorf("opened %d certificates, want %d", opens, want)
				}
				return opens, nil
			}
			// The same bound used below must reject two sequential opens, even
			// when their events are queued before the observer reads them.
			for range 2 {
				if _, err := os.ReadFile(path); err != nil {
					t.Fatal(err)
				}
			}
			if opens, err := observe(1); opens != 2 || err == nil {
				t.Fatalf("two-open control: opened %d certificates, error=%v; want 2 and a bound rejection", opens, err)
			}
			for _, mode := range []string{"warm", "cold", "unregistered"} {
				if mode == "cold" {
					root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
					cold := thread.NewStore(history.New(root))
					t.Cleanup(func() { _ = cold.Shutdown() })
					shadow.store = cold
					installDaemonThreadRelay(&cfg, shadow)
				}
				if mode == "unregistered" {
					shadow.store = store
					shadow.registry.Delete(testConvID)
					installDaemonThreadRelay(&cfg, shadow)
				}
				for range 3 {
					got, present := cfg.ThreadLastShownVersion(testConvID)
					if present != (mode == "warm") || present && got != 1 {
						t.Fatalf("%s: watermark = %d, %t", mode, got, present)
					}
					want := 0
					if mode == "warm" {
						want = 1
					}
					if _, err := observe(want); err != nil {
						t.Fatalf("%s: %v", mode, err)
					}
				}
			}
		})
	}
}
