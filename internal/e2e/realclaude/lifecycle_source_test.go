//go:build e2e_realclaude

package realclaude

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var lifecycleRecordingSource = newLifecycleSourceValidation(func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	closeErr := f.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
})

func newLifecycleSourceValidation(read func() (string, error)) func(string) error {
	// The executable identifies this process for its whole lifetime. Each config
	// must still name that identity; an initial read failure cannot be retried.
	read = sync.OnceValues(read)
	return func(expected string) error {
		digest, err := read()
		if err != nil || digest != expected {
			return os.ErrInvalid
		}
		return nil
	}
}

func TestLifecycleSourceValidationDoesNotRepeatAtSeal(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	digest := strings.Repeat("a", 64)
	validate := newLifecycleSourceValidation(func() (string, error) {
		if reads.Add(1) != 1 {
			return "", errors.New("source read after original assertions")
		}
		close(entered)
		<-release
		return digest, nil
	})
	done := make(chan error, 1)
	go func() { done <- validate(digest) }()
	<-entered
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := validate(digest); err != nil {
		t.Fatal("seal repeated source read:", err)
	}
	if err := validate(strings.Repeat("b", 64)); err == nil {
		t.Fatal("foreign source accepted after validation")
	}
	for range 8 {
		go func() { done <- validate(digest) }()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 1 {
		t.Fatal("repeated executable reads", reads.Load())
	}
}

func TestLifecycleSourceValidationRetainsInitialFailure(t *testing.T) {
	var reads int
	validate := newLifecycleSourceValidation(func() (string, error) {
		reads++
		if reads == 1 {
			return "", os.ErrPermission
		}
		return "source", nil
	})
	if validate("source") == nil || validate("source") == nil || reads != 1 {
		t.Fatal("failed source identity was replaced by a later observation")
	}
}
