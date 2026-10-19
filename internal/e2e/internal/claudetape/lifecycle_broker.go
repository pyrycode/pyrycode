package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// LifecycleTape records a provider killed by an original operator action, or
// a still-live prefix sealed after all original assertions. Captured host PIDs
// never enter replay fixtures. Kill authorization is an ephemeral owned-PID
// witness checked by the broker and the outer original-test parent.
type LifecycleTape struct {
	Version int    `json:"version"`
	Tape    *Tape  `json:"tape"`
	End     string `json:"end"`
	Stderr  string `json:"stderr"`
}

func (t *LifecycleTape) Validate() error {
	if t == nil || t.Version != 1 || t.Tape == nil || (t.End != "kill" && t.End != "seal" && t.End != "eof" && t.End != "delete-conversation") || len(t.Stderr) > MaxBytes || Safe([]byte(t.Stderr)) != nil {
		return errors.New("invalid lifecycle tape")
	}
	raw, err := json.Marshal(t.Tape)
	if err != nil {
		return err
	}
	_, err = Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	whole, err := json.Marshal(t)
	if err != nil || len(whole) > MaxBytes || safeCLIJSON(whole) != nil {
		return errors.New("unsafe lifecycle carrier")
	}
	return nil
}
func DecodeLifecycle(r io.Reader) (*LifecycleTape, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, errors.New("lifecycle tape exceeded limit")
	}
	var t LifecycleTape
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(&t) != nil || d.Decode(new(any)) != io.EOF || t.Validate() != nil {
		return nil, errors.New("invalid lifecycle tape")
	}
	return &t, nil
}
func (t *LifecycleTape) Save(path string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("lifecycle serialization failed")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = f.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err = f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

type LifecycleBrokerConfig struct {
	PoolProcessConfig
	OwnerPID         int
	BrokerParentPID  int
	Deadline         time.Time
	End, Kill, Abort string
	OnSeal           func(int) error `json:"-"`
}
type LifecycleKill struct {
	PID    int
	Signal string
}

// RunLifecycleBroker uses the pipe layout documented by ExecLifecycleChild.
// The broker is outside the provider's process group. A kill-ended role may
// seal only after actual provider stdout AND stderr EOF and exact original
// SIGKILL authorization. A seal-ended role must still be alive at its seal.
func RunLifecycleBroker(c LifecycleBrokerConfig, binding childBinding) (count int, resultErr error) {
	fail := errors.New("lifecycle broker failed")
	parent := c.OwnerPID
	if c.BrokerParentPID != 0 {
		parent = c.BrokerParentPID
	}
	if c.OwnerPID <= 1 || parent <= 1 || os.Getppid() != parent || (c.Mode != "capture" && c.Mode != "replay") || (c.End != "kill" && c.End != "seal" && c.End != "eof" && c.End != "delete-conversation") {
		return 0, fail
	}
	defer func() {
		if resultErr != nil && c.BrokerParentPID == 0 && os.Getppid() == c.OwnerPID {
			_ = syscall.Kill(c.OwnerPID, syscall.SIGKILL)
		}
	}()
	files, err := binding.Files()
	if err != nil {
		return 0, err
	}
	// Register inherited pipe writers with the runtime poller before wrapping
	// them. NewFile only offers deadlines when the descriptor is nonblocking.
	for _, fd := range []int{4, 5, 6} {
		if syscall.SetNonblock(fd, true) != nil {
			return 0, fail
		}
	}
	originals := []*os.File{os.NewFile(3, "original-input"), os.NewFile(4, "original-output"), os.NewFile(5, "original-stderr"), os.NewFile(6, "provider-input"), os.NewFile(7, "provider-output"), os.NewFile(8, "provider-stderr"), os.NewFile(9, "ready")}
	for _, f := range originals {
		defer f.Close()
	}
	claim, err := os.OpenFile(c.Claim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.WriteFile(c.Claim+".duplicate", nil, 0600)
		return 0, fail
	}
	if claim.Close() != nil {
		return 0, fail
	}
	var tape *LifecycleTape
	var player *Player
	if c.Mode == "capture" {
		version := 1
		if c.End == "delete-conversation" {
			version = 2
		}
		tape = &LifecycleTape{Version: 1, Tape: &Tape{Version: version, Scenario: c.Scenario, Producer: c.Producer, Args: binding.Args(), Files: files}, End: c.End}
	} else {
		f, e := os.Open(c.Path)
		if e != nil {
			return 0, fail
		}
		tape, e = DecodeLifecycle(f)
		_ = f.Close()
		if e != nil || tape.End != c.End || tape.Tape.Scenario != c.Scenario {
			return 0, fail
		}
		if tape.Tape.CheckFiles(files) != nil {
			return 0, fail
		}
		player, e = New(tape.Tape, binding.Args())
		if e != nil {
			return 0, e
		}
	}
	// Cache FDs before setting nonblocking; File.Fd can itself restore blocking.
	inputFD, outFD, errFD := int(originals[0].Fd()), int(originals[4].Fd()), int(originals[5].Fd())
	for _, fd := range []int{inputFD, outFD, errFD} {
		if syscall.SetNonblock(fd, true) != nil {
			return 0, fail
		}
	}
	if _, err = originals[6].Write([]byte{1}); err != nil {
		return 0, fail
	}
	_ = originals[6].Close()
	frames, total := 0, 0
	sealed := false
	actionAccepted := false
	var observeAction func() error
	sealDeadline := time.Time{}
	deadline := time.Now().Add(20 * time.Minute)
	if !c.Deadline.IsZero() && c.Deadline.Before(deadline) {
		deadline = c.Deadline
	}
	expired := func() bool { return !time.Now().Before(deadline) || (sealed && !time.Now().Before(sealDeadline)) }
	write := func(f *os.File, raw []byte) error {
		if len(raw) == 0 {
			return nil
		}
		if expired() {
			return errors.New("lifecycle forwarding deadline exceeded")
		}
		writeDeadline := time.Now().Add(10 * time.Second)
		if deadline.Before(writeDeadline) {
			writeDeadline = deadline
		}
		if sealed && sealDeadline.Before(writeDeadline) {
			writeDeadline = sealDeadline
		}
		if f.SetWriteDeadline(writeDeadline) != nil {
			return fail
		}
		_, err := f.Write(raw)
		return err
	}
	writeOutput := func(raw []byte) error {
		restored, e := binding.Frame(raw, true)
		if e != nil {
			return e
		}
		e = write(originals[1], append(restored, '\n'))
		if e == nil {
			frames++
		}
		return e
	}
	if player != nil {
		for _, raw := range player.Prefix() {
			if err := writeOutput(raw); err != nil {
				return frames, fail
			}
		}
	}
	var pendingInput, pendingOutput []byte
	inputEOF, outputEOF, stderrEOF := false, false, false
	accept := func(direction string, raw []byte) error {
		if direction == "stdout" && observeAction != nil {
			if err := observeAction(); err != nil {
				return err
			}
		}
		if sealed {
			target := originals[1]
			if direction == "stdin" {
				target = originals[3]
			} else if c.Mode == "replay" {
				return fail
			}
			return write(target, append(raw, '\n'))
		}
		normalized, e := binding.Frame(raw, false)
		if e != nil {
			return e
		}
		if player != nil {
			if direction != "stdin" {
				return fail
			}
			released, e := player.Input(normalized)
			if e != nil {
				return e
			}
			frames++
			// Consume through the stand-in's pipe as well: closure and the owned PID
			// have real process semantics even though provider responses are replayed.
			if e = write(originals[3], append(raw, '\n')); e != nil {
				return fail
			}
			for _, v := range released {
				if e = writeOutput(v); e != nil {
					return e
				}
			}
			return nil
		}
		total += len(normalized)
		if total > MaxBytes || len(tape.Tape.Frames) >= MaxFrames || Safe(normalized) != nil {
			return fail
		}
		tape.Tape.Frames = append(tape.Tape.Frames, Frame{Direction: direction, Data: normalized})
		frames++
		target := originals[1]
		if direction == "stdin" {
			target = originals[3]
		}
		return write(target, append(raw, '\n'))
	}
	read := func(fd int, pending *[]byte, eof *bool, direction string) error {
		if *eof {
			return nil
		}
		var buf [4096]byte
		quantum := 0
		for {
			if expired() {
				return errors.New("lifecycle forwarding deadline exceeded")
			}
			n, e := syscall.Read(fd, buf[:])
			if n > 0 {
				quantum += n
				if direction == "stderr" {
					if player != nil {
						return fail
					}
					if !sealed {
						tape.Stderr += string(buf[:n])
					}
					if len(tape.Stderr) > MaxBytes {
						return fail
					}
					if e := write(originals[2], buf[:n]); e != nil {
						return fail
					}
				} else {
					*pending = append(*pending, buf[:n]...)
					for {
						end := bytes.IndexByte(*pending, '\n')
						if end < 0 {
							break
						}
						if end > MaxBytes {
							return fail
						}
						if e := accept(direction, (*pending)[:end]); e != nil {
							return e
						}
						*pending = (*pending)[end+1:]
					}
					if len(*pending) > MaxBytes {
						return fail
					}
				}
			}
			if quantum >= 65536 {
				return nil
			}
			if e == syscall.EINTR {
				continue
			}
			if e == syscall.EAGAIN || e == syscall.EWOULDBLOCK {
				return nil
			}
			if e != nil {
				return fail
			}
			if n == 0 {
				*eof = true
				if direction == "stdin" {
					_ = originals[3].Close()
				}
				return nil
			}
		}
	}
	authorizedKill := func() bool {
		raw, e := runnerPrivateRead(c.Kill, 16384)
		if e != nil {
			return false
		}
		var k LifecycleKill
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		return uniqueSuiteKeys(raw) && d.Decode(&k) == nil && d.Decode(new(any)) == io.EOF && k.PID == c.OwnerPID && k.Signal == func() string {
			if c.End == "delete-conversation" {
				return "delete-conversation"
			}
			return "SIGKILL"
		}()
	}

	observeAction = func() error {
		if c.End != "delete-conversation" || actionAccepted {
			return nil
		}
		if _, err := os.Lstat(c.Kill); os.IsNotExist(err) {
			return nil
		}
		if !authorizedKill() {
			return errors.New("lifecycle action has foreign owner")
		}
		if len(pendingInput) != 0 {
			return nil
		}
		if player != nil && (player.next >= len(player.tape.Frames) || player.tape.Frames[player.next].Direction != ClientCheckpointDirection) {
			return nil
		}
		checkpoint, _ := json.Marshal(ClientCheckpoint{Type: ClientCheckpointDirection, Sequence: 1, Action: "lifecycle_delete", ConversationID: "${LIFECYCLE_CONVERSATION}"})
		if player != nil {
			released, err := player.Action(checkpoint)
			if err != nil {
				return err
			}
			for _, raw := range released {
				if err := writeOutput(raw); err != nil {
					return err
				}
			}
		} else {
			total += len(checkpoint)
			if total > MaxBytes || len(tape.Tape.Frames) >= MaxFrames {
				return fail
			}
			tape.Tape.Frames = append(tape.Tape.Frames, Frame{Direction: ClientCheckpointDirection, Data: checkpoint})
		}
		frames++
		actionAccepted = true
		if err := PublishLifecycleControl(c.Kill+".ready", LifecycleKill{PID: c.OwnerPID, Signal: "delete-conversation"}); err != nil {
			return fail
		}
		return nil
	}
	finish := func() (int, error) {
		if c.End == "delete-conversation" && !actionAccepted {
			return frames, errors.New("missing original delete checkpoint")
		}
		if len(pendingInput) != 0 || len(pendingOutput) != 0 {
			return frames, fail
		}
		if player != nil {
			if player.Complete() != nil {
				return frames, fail
			}
			if e := write(originals[2], []byte(tape.Stderr)); e != nil {
				return frames, fail
			}
			return frames, nil
		}
		return frames, tape.Save(c.Path)
	}
	for time.Now().Before(deadline) {
		if c.Abort != "" {
			if _, err := os.Lstat(c.Abort); err == nil {
				return frames, errors.New("lifecycle owner aborted recording")
			}
		}
		if err := read(inputFD, &pendingInput, &inputEOF, "stdin"); err != nil {
			return frames, err
		}
		if err := observeAction(); err != nil {
			return frames, err
		}
		if player != nil {
			// A replay stand-in must not produce its own output.
			if err := read(outFD, &pendingOutput, &outputEOF, "unexpected"); err != nil {
				return frames, err
			}
		} else if err := read(outFD, &pendingOutput, &outputEOF, "stdout"); err != nil {
			return frames, err
		}
		var unused []byte
		if err := read(errFD, &unused, &stderrEOF, "stderr"); err != nil {
			return frames, err
		}
		if sealed {
			// The owner may keep its stdin writer open until Wait has drained
			// stdout/stderr. Waiting for that input EOF here creates a shutdown
			// cycle; complete provider output EOF releases the original drains.
			if outputEOF && stderrEOF {
				return frames, nil
			}
			if time.Now().After(sealDeadline) {
				return frames, errors.New("original owner did not close sealed provider")
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if c.End == "eof" && inputEOF && outputEOF && stderrEOF {
			return finish()
		}
		if c.End == "kill" && outputEOF && stderrEOF {
			if !authorizedKill() {
				return frames, errors.New("provider ended without original owned kill")
			}
			return finish()
		}
		if _, e := os.Lstat(c.Finish); e == nil {
			if (c.End != "seal" && c.End != "delete-conversation") || outputEOF || stderrEOF || inputEOF {
				return frames, errors.New("provider not live at prefix seal")
			}
			if c.OnSeal == nil {
				return frames, errors.New("missing immutable seal callback")
			}
			n, e := finish()
			if e != nil {
				return n, e
			}
			if e = c.OnSeal(n); e != nil {
				return n, e
			}
			sealed = true
			sealDeadline = time.Now().Add(30 * time.Second)
			continue
		}
		if (outputEOF || stderrEOF || inputEOF) && (c.End == "seal" || c.End == "delete-conversation") {
			return frames, errors.New("provider ended before prefix seal")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return frames, errors.New("lifecycle broker deadline exceeded")
}
