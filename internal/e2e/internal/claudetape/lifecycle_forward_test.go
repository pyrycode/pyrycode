package claudetape

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

func TestLifecycleForwardObserverCannotChangeOrHideActualWrite(t *testing.T) {
	literal := []byte(` {"type":"user","message":{"content":"literal <>&"}} `)
	for _, mutation := range []string{"normal", "mutate-copy", "omit", "duplicate", "hide-write-error", "observer-error"} {
		t.Run(mutation, func(t *testing.T) {
			calls := 0
			var written []byte
			err := forwardLifecycleFrame(literal, func(observed []byte, forward func() error) error {
				if mutation == "mutate-copy" {
					observed[0] = 'x'
				}
				if mutation == "omit" {
					return nil
				}
				err := forward()
				if mutation == "duplicate" {
					_ = forward()
					return nil
				}
				if mutation == "hide-write-error" {
					return nil
				}
				if mutation == "observer-error" {
					return errors.New("observer rejected")
				}
				return err
			}, func(raw []byte) error {
				calls++
				written = append([]byte(nil), raw...)
				if mutation == "hide-write-error" {
					return io.ErrClosedPipe
				}
				return nil
			})
			valid := mutation == "normal" || mutation == "mutate-copy"
			if (err == nil) != valid {
				t.Fatal("actual forwarding contract mismatch")
			}
			if mutation != "omit" && (calls != 1 || !bytes.Equal(written, literal)) {
				t.Fatal("captured source changed or actual write duplicated")
			}
		})
	}
}
func TestLifecycleReadyCarriesPrivateSetupOnlyAfterExactMarker(t *testing.T) {
	for _, mutation := range []string{"legacy", "setup", "bad-marker", "partial-setup", "rejected-setup"} {
		t.Run(mutation, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			data := []byte{1}
			if mutation == "bad-marker" {
				data[0] = 2
			}
			if mutation == "setup" || mutation == "rejected-setup" {
				data = append(data, []byte("controlled-private-setup")...)
			}
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
			w.Close()
			var setup func(io.Reader) error
			if mutation != "legacy" {
				setup = func(reader io.Reader) error {
					raw, err := io.ReadAll(reader)
					if err != nil || string(raw) != "controlled-private-setup" || mutation == "rejected-setup" {
						return errors.New("setup rejected")
					}
					return nil
				}
			}
			err = receiveLifecycleReady(r, setup)
			valid := mutation == "legacy" || mutation == "setup"
			if (err == nil) != valid {
				t.Fatal("private readiness contract mismatch")
			}
		})
	}
}
