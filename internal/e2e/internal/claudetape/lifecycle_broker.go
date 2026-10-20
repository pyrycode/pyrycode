package claudetape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// LifecycleTape records a provider killed by an original operator action, or
// a still-live prefix sealed after all original assertions. Ownership and action
// PIDs stay in private launch evidence. Native protocol PID metadata remains
// literal, opaque data and is never used as a process or signal target. Kill
// authorization uses the current owned launch, checked by the outer test parent.
type LifecycleTape struct {
	Timeline *LifecycleTimeline `json:"timeline,omitempty"`
	Version  int                `json:"version"`
	Tape     *Tape              `json:"tape"`
	End      string             `json:"end"`
	Stderr   string             `json:"stderr"`
}

func (t *LifecycleTape) Validate() error {
	if t == nil || t.Version != 1 || t.Tape == nil || (t.End != "kill" && t.End != "seal" && t.End != "eof" && t.End != "delete-conversation" && t.End != "rotate-session" && t.End != "idle-evict") || len(t.Stderr) > MaxBytes || Safe([]byte(t.Stderr)) != nil {
		return errors.New("invalid lifecycle tape")
	}
	if validateLifecycleTimeline(t) != nil {
		return errors.New("invalid native output timeline")
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
	Context          context.Context `json:"-"`
	OwnerPID         int
	BrokerParentPID  int
	Deadline         time.Time
	End, Kill, Abort string
	Ready            func(io.Writer) error            `json:"-"`
	ForwardInput     func([]byte, func() error) error `json:"-"`
	ForwardOutput    func([]byte, func() error) error `json:"-"`
	ReplayOutput     func([]byte) ([]byte, error)     `json:"-"`
	OnSeal           func(int) error                  `json:"-"`
	OnInput          func([]byte) error               `json:"-"`
	BeforeOutput     func([]byte) error               `json:"-"`
	BeforeSeal       func(*Tape) error                `json:"-"`
	OnFailure        func(*LifecycleTape, int)        `json:"-"`
	OnTerminalState  func(bool, bool, bool, bool)     `json:"-"`
	OnProgress       func(LifecycleProgress)          `json:"-"`
}

// LifecycleProgress contains terminal observations, never protocol content or
// ownership authority. Cursor counts consumed fixture frames, including outputs
// that may not yet have been forwarded.
type LifecycleProgress struct {
	Stage, ErrorClass                                      string
	Frames, Cursor, PendingInput                           int
	ActionAccepted, InputEOF, OutputEOF, StderrEOF, Sealed bool
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
	startup := time.Now()
	fail := errors.New("lifecycle broker failed")
	if c.Context != nil && c.Context.Err() != nil {
		return 0, fail
	}
	parent := c.OwnerPID
	if c.BrokerParentPID != 0 {
		parent = c.BrokerParentPID
	}
	if c.Mode == "capture" && c.ReplayOutput != nil {
		return 0, fail
	}
	if c.OwnerPID <= 1 || parent <= 1 || os.Getppid() != parent || (c.Mode != "capture" && c.Mode != "replay") || (c.End != "kill" && c.End != "seal" && c.End != "eof" && c.End != "delete-conversation" && c.End != "rotate-session" && c.End != "idle-evict") {
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
	for _, fd := range []int{4, 5, 6, 9} {
		if syscall.SetNonblock(fd, true) != nil {
			return 0, fail
		}
	}
	originals := []*os.File{os.NewFile(3, "original-input"), os.NewFile(4, "original-output"), os.NewFile(5, "original-stderr"), os.NewFile(6, "provider-input"), os.NewFile(7, "provider-output"), os.NewFile(8, "provider-stderr"), os.NewFile(9, "ready")}
	for _, f := range originals {
		defer f.Close()
	}
	if c.Context != nil {
		stopped := make(chan struct{})
		defer close(stopped)
		go func() {
			select {
			case <-c.Context.Done():
				// Only Go-managed pollable writers can be blocked under a forwarding
				// mutex. Keep raw-read descriptors open until this loop returns, so a
				// cached syscall.Read fd cannot be recycled underneath the loop.
				for _, i := range []int{1, 2, 3, 6} {
					originals[i].Close()
				}
			case <-stopped:
			}
		}()
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
	defer func() {
		if resultErr != nil && tape != nil && c.OnFailure != nil {
			c.OnFailure(tape, count)
		}
	}()
	var player *Player
	if c.Mode == "capture" {
		version := 1
		if LifecycleAction(c.End) != "" {
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
	if lifecycleTimelineEnabled(c.Scenario) {
		if c.Mode == "capture" {
			tape.Timeline = &LifecycleTimeline{Kind: lifecycleTimelineKind, OutputAfterNS: []int64{}}
			if lifecycleInputTimeline(c.Scenario) {
				tape.Timeline.Kind = lifecycleInputTimelineKind
				tape.Timeline.OutputAfterUser = []int{}
			}
			if lifecycleFrameTimeline(c.Scenario) {
				tape.Timeline.Kind = lifecycleFrameTimelineKind
				tape.Timeline.OutputAfterInput = []int{}
			}
		} else if tape.Timeline == nil {
			return 0, errors.New("missing required native output timeline")
		}
	}
	// Cache FDs before setting nonblocking; File.Fd can itself restore blocking.
	inputFD, outFD, errFD := int(originals[0].Fd()), int(originals[4].Fd()), int(originals[5].Fd())
	for _, fd := range []int{inputFD, outFD, errFD} {
		if syscall.SetNonblock(fd, true) != nil {
			return 0, fail
		}
	}
	observeCompletion := NewConversationCompletionObserver(c, tape.Tape)
	timelineOrigin := time.Now()
	startupNS := timelineOrigin.Sub(startup).Nanoseconds()
	var completionNS, witnessNS, outputCompletionNS int64
	timelineOrigins := []time.Time{timelineOrigin}
	recordTimelineInput := func(raw []byte) {
		if lifecycleFrameTimeline(c.Scenario) || lifecycleTimelineUser(raw) {
			timelineOrigins = append(timelineOrigins, time.Now())
		}
	}
	timelineOutput := 0
	if _, err = originals[6].Write([]byte{1}); err != nil {
		return 0, fail
	}
	if c.Ready != nil {
		if err := c.Ready(originals[6]); err != nil {
			return 0, fail
		}
	}
	_ = originals[6].Close()
	frames, total := 0, 0
	sealed := false
	actionAccepted := false
	var pendingInput, pendingOutput []byte
	inputEOF, outputEOF, stderrEOF := false, false, false
	stage := "ready"
	defer func() {
		if c.OnProgress != nil {
			class := "none"
			if resultErr != nil {
				class = "boundary"
				if errors.Is(resultErr, syscall.EPIPE) {
					class = "broken-pipe"
				}
			}
			cursor := 0
			if player != nil {
				cursor = player.next
			}
			c.OnProgress(LifecycleProgress{stage, class, count, cursor, len(pendingInput), actionAccepted, inputEOF, outputEOF, stderrEOF, sealed})
		}
	}()
	var observeAction func() error
	sealDeadline := time.Time{}
	deadline := time.Now().Add(20 * time.Minute)
	if !c.Deadline.IsZero() && c.Deadline.Before(deadline) {
		deadline = c.Deadline
	}
	expired := func() bool {
		return c.Context != nil && c.Context.Err() != nil || !time.Now().Before(deadline) || (sealed && !time.Now().Before(sealDeadline))
	}
	writeBounded := func(f *os.File, raw []byte, extraDeadline time.Time) error {
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
		if !extraDeadline.IsZero() && extraDeadline.Before(writeDeadline) {
			writeDeadline = extraDeadline
		}
		if f.SetWriteDeadline(writeDeadline) != nil {
			return fail
		}
		_, err := f.Write(raw)
		return err
	}
	write := func(f *os.File, raw []byte) error { return writeBounded(f, raw, time.Time{}) }
	writeOutput := func(raw []byte) error {
		stage = "output-forward"
		emit := func(extra time.Time) error {
			restored, err := binding.Frame(raw, true)
			if err != nil {
				return err
			}
			if c.BeforeOutput != nil {
				if err := c.BeforeOutput(restored); err != nil {
					return err
				}
			}
			if c.ReplayOutput != nil {
				restored, err = c.ReplayOutput(append([]byte(nil), restored...))
				if err != nil {
					return err
				}
			}
			return forwardLifecycleFrame(restored, c.ForwardOutput, func(actual []byte) error { return writeBounded(originals[1], append(actual, '\n'), extra) })
		}
		if tape.Timeline != nil {
			if timelineOutput >= len(tape.Timeline.OutputAfterNS) {
				return fail
			}
			origin := timelineOrigin
			anchor := 0
			if tape.Timeline.Kind == lifecycleInputTimelineKind || tape.Timeline.Kind == lifecycleFrameTimelineKind {
				anchors := tape.Timeline.OutputAfterUser
				if tape.Timeline.Kind == lifecycleFrameTimelineKind {
					anchors = tape.Timeline.OutputAfterInput
				}
				if timelineOutput >= len(anchors) {
					return fail
				}
				anchor = anchors[timelineOutput]
				if anchor < 0 || anchor >= len(timelineOrigins) {
					return fail
				}
				origin = timelineOrigins[anchor]
			}
			offset := tape.Timeline.OutputAfterNS[timelineOutput]
			if err := forwardLifecycleOutput(origin, offset, time.Now, time.Sleep, func() error { return emit(origin.Add(time.Duration(offset)).Add(100 * time.Millisecond)) }); err != nil {
				var timing *LifecycleTimingError
				if errors.As(err, &timing) {
					timing.Output, timing.Anchor = timelineOutput, anchor
					timing.StartupNS, timing.ReadyNS = startupNS, time.Since(timelineOrigin).Nanoseconds()
					timing.CompletionNS, timing.WitnessNS = completionNS, witnessNS
					timing.PriorOutputCompletionNS = outputCompletionNS
				}
				return err
			}
			timelineOutput++
		} else if err := emit(time.Time{}); err != nil {
			return err
		}
		frames++
		work := time.Now()
		err := observeCompletion("stdout", raw)
		outputCompletionNS = time.Since(work).Nanoseconds()
		return err
	}
	if player != nil {
		for _, raw := range player.Prefix() {
			if err := writeOutput(raw); err != nil {
				return frames, err
			}
		}
	}
	defer func() {
		if c.OnTerminalState != nil {
			c.OnTerminalState(inputEOF, outputEOF, stderrEOF, sealed)
		}
	}()
	accept := func(direction string, raw []byte) error {
		stage = "accept-" + direction
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
			inputForward := func() error { return write(originals[3], append(raw, '\n')) }
			stage = "input-forward"
			e = forwardLifecycleFrame(raw, c.ForwardInput, func([]byte) error { return inputForward() })
			if e != nil {
				return e
			}
			recordTimelineInput(normalized)
			work := time.Now()
			if e := observeCompletion("stdin", normalized); e != nil {
				return e
			}
			completionNS = time.Since(work).Nanoseconds()
			work = time.Now()
			if c.OnInput != nil {
				if e := c.OnInput(normalized); e != nil {
					return e
				}
				witnessNS = time.Since(work).Nanoseconds()
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
		if direction == "stdout" && c.BeforeOutput != nil {
			if err := c.BeforeOutput(raw); err != nil {
				return err
			}
		}
		callback := c.ForwardOutput
		if direction == "stdin" {
			callback = c.ForwardInput
		}
		if err := forwardLifecycleFrame(raw, callback, func(actual []byte) error { return write(target, append(actual, '\n')) }); err != nil {
			return err
		}

		if direction == "stdout" && tape.Timeline != nil {
			origin := timelineOrigin
			if tape.Timeline.Kind == lifecycleInputTimelineKind || tape.Timeline.Kind == lifecycleFrameTimelineKind {
				anchor := len(timelineOrigins) - 1
				if tape.Timeline.Kind == lifecycleFrameTimelineKind {
					tape.Timeline.OutputAfterInput = append(tape.Timeline.OutputAfterInput, anchor)
				} else {
					tape.Timeline.OutputAfterUser = append(tape.Timeline.OutputAfterUser, anchor)
				}
				origin = timelineOrigins[anchor]
			}
			tape.Timeline.OutputAfterNS = append(tape.Timeline.OutputAfterNS, time.Since(origin).Nanoseconds())
		}
		if direction == "stdin" {
			recordTimelineInput(normalized)
		}
		if err := observeCompletion(direction, normalized); err != nil {
			return err
		}
		if direction == "stdin" && c.OnInput != nil {
			return c.OnInput(normalized)
		}
		return nil
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
			if LifecycleAction(c.End) != "" {
				return c.End
			}
			return "SIGKILL"
		}()
	}

	observeAction = func() error {
		if LifecycleAction(c.End) == "" || actionAccepted {
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
		checkpoint, _ := json.Marshal(ClientCheckpoint{Type: ClientCheckpointDirection, Sequence: 1, Action: LifecycleAction(c.End), ConversationID: "${LIFECYCLE_CONVERSATION}"})
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
		stage = "action-ack"
		if err := PublishLifecycleControl(c.Kill+".ready", LifecycleKill{PID: c.OwnerPID, Signal: c.End}); err != nil {
			return fail
		}
		return nil
	}
	finish := func() (int, error) {
		stage = "finish"
		if c.Context != nil && c.Context.Err() != nil {
			return frames, errors.New("cancelled lifecycle cannot seal")
		}
		if LifecycleAction(c.End) != "" && !actionAccepted {
			return frames, errors.New("missing original delete checkpoint")
		}
		if len(pendingInput) != 0 || len(pendingOutput) != 0 {
			return frames, fail
		}
		if player != nil {
			if tape.Timeline != nil && timelineOutput != len(tape.Timeline.OutputAfterNS) {
				return frames, fail
			}
			if player.Complete() != nil {
				return frames, fail
			}
			if e := write(originals[2], []byte(tape.Stderr)); e != nil {
				return frames, fail
			}
			if c.BeforeSeal != nil {
				if err := c.BeforeSeal(tape.Tape); err != nil {
					return frames, err
				}
			}
			if c.Context != nil && c.Context.Err() != nil {
				return frames, errors.New("cancelled lifecycle cannot finish")
			}
			return frames, nil
		}
		if c.BeforeSeal != nil {
			if err := c.BeforeSeal(tape.Tape); err != nil {
				return frames, err
			}
		}
		if c.Context != nil && c.Context.Err() != nil {
			return frames, errors.New("cancelled lifecycle cannot publish")
		}
		return frames, tape.Save(c.Path)
	}
	for time.Now().Before(deadline) {
		if c.Context != nil && c.Context.Err() != nil {
			return frames, errors.New("lifecycle forwarding context cancelled")
		}
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
		if (c.End == "kill" || c.End == "rotate-session" || c.End == "idle-evict") && outputEOF && stderrEOF {
			if !authorizedKill() {
				return frames, errors.New("provider ended without original owned kill")
			}
			return finish()
		}
		if _, e := os.Lstat(c.Finish); e == nil {
			if !LifecycleSeals(c.End) || outputEOF || stderrEOF || inputEOF {
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
		if (outputEOF || stderrEOF || inputEOF) && LifecycleSeals(c.End) {
			return frames, errors.New("provider ended before prefix seal")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return frames, errors.New("lifecycle broker deadline exceeded")
}

// Trusted synchronous observers receive a copy and must invoke the supplied
// actual write exactly once. They cannot silently mutate a captured frame,
// omit/duplicate a forward, or hide an underlying pipe error.
func forwardLifecycleFrame(raw []byte, observe func([]byte, func() error) error, write func([]byte) error) error {
	if observe == nil {
		return write(raw)
	}
	calls := 0
	var writeErr error
	forward := func() error {
		calls++
		if calls != 1 {
			writeErr = errors.New("repeated lifecycle forward")
			return writeErr
		}
		writeErr = write(raw)
		return writeErr
	}
	err := observe(append([]byte(nil), raw...), forward)
	if err != nil {
		return err
	}
	if calls != 1 || writeErr != nil {
		return errors.New("incomplete lifecycle forward")
	}
	return nil
}
