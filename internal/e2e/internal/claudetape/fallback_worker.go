package claudetape

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// FallbackWorkerTape is a finite print-mode child, including actual stdin EOF,
// both output EOFs and status from the native child's owning exec.Cmd.Wait.
// PIDs/process groups are private launch capabilities, never fixture values.
type FallbackWorkerTape struct {
	DirectoryMode  uint32               `json:"directory_mode"`
	DirectoryEmpty bool                 `json:"directory_empty"`
	Version        int                  `json:"version"`
	Producer       Producer             `json:"producer"`
	Args           []string             `json:"args"`
	Input          string               `json:"input"`
	InputEOF       bool                 `json:"input_eof"`
	Frames         []CLIFrame           `json:"frames"`
	StdoutEOF      bool                 `json:"stdout_eof"`
	StderrEOF      bool                 `json:"stderr_eof"`
	Status         FallbackWorkerStatus `json:"status"`
}
type FallbackWorkerStatus struct{ ExitCode, Signal int }

// FallbackProductionWait preserves a cancellation of the production wrapper
// after the separately owned native child has already exited successfully.
// It never replaces the native tape or its independently owned receipt.
type FallbackProductionWait struct {
	Status      FallbackWorkerStatus `json:"status"`
	GroupCancel bool                 `json:"group_cancel"`
}

func ValidateFallbackProductionWait(native FallbackWorkerStatus, wait *FallbackProductionWait) error {
	if wait != nil && (native != (FallbackWorkerStatus{ExitCode: 0}) || wait.Status != (FallbackWorkerStatus{ExitCode: -1, Signal: int(syscall.SIGKILL)}) || !wait.GroupCancel) {
		return errors.New("unsupported native/production wait relationship")
	}
	return nil
}

type FallbackWorkerJob struct {
	ReplayWait                                                       *FallbackProductionWait `json:",omitempty"`
	DaemonPID                                                        int
	Version, OwnerPID, GroupPID, BrokerParentPID                     int
	Scenario, Home, WorkDir, Claude, Path, Abort, SourceBinarySHA256 string
	Args                                                             []string
	Producer                                                         Producer
}
type FallbackWorkerReceipt struct {
	TapeSHA256                                        string
	Version, OwnerPID, GroupPID, NativePID, BrokerPID int
	Success                                           bool
	Frames                                            int
	Status                                            FallbackWorkerStatus
}

func fallbackWorkerArgs(args []string) error {
	fail := errors.New("unreviewed finite fallback invocation")
	values := map[string]string{"--model": "haiku", "--fallback-model": "", "--max-turns": "1", "--output-format": "stream-json", "--tools": "", "--mcp-config": `{"mcpServers":{}}`, "--setting-sources": "", "--settings": `{"disableAllHooks":true,"autoMemoryEnabled":false}`}
	switches := map[string]bool{"--print": true, "--verbose": true, "--disable-slash-commands": true, "--strict-mcp-config": true, "--no-session-persistence": true}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if seen[flag] {
			return fail
		}
		seen[flag] = true
		if switches[flag] {
			continue
		}
		if flag == "--system-prompt" {
			if i+1 >= len(args) || args[i+1] == "" || !utf8.ValidString(args[i+1]) || len(args[i+1]) > 16384 || Safe([]byte(args[i+1])) != nil {
				return fail
			}
			i++
			continue
		}
		value, ok := values[flag]
		if !ok || i+1 >= len(args) || args[i+1] != value {
			return fail
		}
		i++
	}
	if len(seen) != len(values)+len(switches)+1 {
		return fail
	}
	return nil
}
func (t *FallbackWorkerTape) Validate() error {
	fail := errors.New("invalid finite fallback tape")
	if t == nil || t.Version != 1 || t.DirectoryMode != 0700 || !t.DirectoryEmpty || fallbackWorkerArgs(t.Args) != nil || !t.InputEOF || !t.StdoutEOF || !t.StderrEOF || len(t.Input) == 0 || len(t.Input) > 32768 || !utf8.ValidString(t.Input) || !uniqueSuiteKeys([]byte(t.Input)) || safeCLIJSON([]byte(t.Input)) != nil || len(t.Frames) > MaxFrames {
		return fail
	}
	var exchange FallbackExchange
	decoder := json.NewDecoder(strings.NewReader(t.Input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&exchange) != nil || decoder.Decode(new(any)) != io.EOF || exchange.User == "" || exchange.Assistant == "" {
		return fail
	}
	if t.Status.Signal == 0 {
		if t.Status.ExitCode < 0 || t.Status.ExitCode > 255 {
			return fail
		}
	} else if t.Status.ExitCode != -1 || t.Status.Signal != int(syscall.SIGKILL) {
		return fail
	}
	for _, item := range []struct {
		raw string
		n   int
	}{{t.Producer.Revision, 20}, {t.Producer.BinarySHA256, 32}} {
		raw, err := hex.DecodeString(item.raw)
		if err != nil || len(raw) != item.n {
			return fail
		}
	}
	if t.Producer.ClaudeVersion == "" {
		return fail
	}
	if _, err := time.Parse(time.RFC3339Nano, t.Producer.CapturedAt); err != nil {
		return fail
	}
	for _, frame := range t.Frames {
		if frame.Data == "" || !utf8.ValidString(frame.Data) {
			return fail
		}
		switch frame.Stream {
		case "stdout":
			if safeCLIJSON([]byte(strings.TrimSpace(frame.Data))) != nil {
				return fail
			}
		case "stderr":
			if Safe([]byte(frame.Data)) != nil {
				return fail
			}
		default:
			return fail
		}
	}
	raw, err := json.Marshal(t)
	if err != nil || len(raw) > MaxBytes || safeCLIJSON(raw) != nil {
		return fail
	}
	return nil
}
func DecodeFallbackWorker(r io.Reader) (*FallbackWorkerTape, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes || !uniqueSuiteKeys(raw) {
		return nil, errors.New("invalid finite worker carrier")
	}
	var tape FallbackWorkerTape
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&tape) != nil || d.Decode(new(any)) != io.EOF || tape.Validate() != nil {
		return nil, errors.New("invalid finite worker carrier")
	}
	return &tape, nil
}
func (t *FallbackWorkerTape) Save(path string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("finite worker exceeded limit")
	}
	return publishClosedFile(filepath.Dir(path), filepath.Base(path), append(raw, '\n'))
}
func workerText(text, home, cwd string, restore bool) string {
	for _, item := range []struct{ actual, label string }{{cwd, "${FALLBACK_CWD}"}, {home, "${FALLBACK_HOME}"}} {
		if restore {
			text = strings.ReplaceAll(text, item.label, item.actual)
		} else {
			text = strings.ReplaceAll(text, item.actual, item.label)
		}
	}
	return text
}

// RunFallbackWorkerBroker runs in the outer parent's separately owned process
// group. fd7 carries the helper's exact current environment only in memory;
// credentials are never included in jobs, tapes, receipts or diagnostics.
// fd3/4/5 are original worker stdin/stdout/stderr, fd9 the status writer. The
// other two pipe capabilities stay distinct and are closed unused.
func RunFallbackWorkerBroker(job FallbackWorkerJob, mode string) error {
	return runFallbackWorkerBroker(job, mode, 45*time.Second)
}
func runFallbackWorkerBroker(job FallbackWorkerJob, mode string, budget time.Duration) (resultErr error) {
	stage := "validate"
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("finite fallback broker stage=%s", stage)
		}
	}()
	fail := errors.New("finite fallback broker failed")
	if job.Version != 1 || job.OwnerPID <= 1 || !fallbackWorkerGroupShape(job) || job.BrokerParentPID != os.Getppid() || (mode != "capture" && mode != "replay") || ValidateFallbackProductionWait(FallbackWorkerStatus{ExitCode: 0}, job.ReplayWait) != nil || fallbackWorkerArgs(job.Args) != nil || mode == "capture" && job.ReplayWait != nil {
		return fail
	}
	for fd := 3; fd <= 9; fd++ {
		syscall.CloseOnExec(fd)
	}
	for _, fd := range []int{6, 8} {
		_ = syscall.Close(fd)
	}
	for _, fd := range []int{3, 4, 5, 7, 9} {
		if syscall.SetNonblock(fd, true) != nil {
			return fail
		}
	}
	input, output, diagnostic, environment, statusPipe := os.NewFile(3, "worker-input"), os.NewFile(4, "worker-output"), os.NewFile(5, "worker-stderr"), os.NewFile(7, "worker-environment"), os.NewFile(9, "worker-status")
	defer input.Close()
	defer output.Close()
	defer diagnostic.Close()
	defer environment.Close()
	defer statusPipe.Close()
	// These are inherited pipes. Pollable writers make native cancellation and
	// disappearance of the original stdout consumer a bounded recording failure.
	readClosed := func(file *os.File, limit int) ([]byte, error) {
		if file.SetReadDeadline(time.Now().Add(10*time.Second)) != nil {
			return nil, fail
		}
		raw, err := io.ReadAll(io.LimitReader(file, int64(limit+1)))
		if err != nil || len(raw) > limit {
			return nil, fail
		}
		return raw, nil
	}
	stage = "read-environment"
	envRaw, err := readClosed(environment, 1024*1024)
	if err != nil {
		return fail
	}
	var env []string
	if json.Unmarshal(envRaw, &env) != nil {
		return fail
	}
	envRaw = nil
	seen := map[string]bool{}
	home := ""
	credential := false
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || seen[key] {
			return fail
		}
		seen[key] = true
		if key == "HOME" {
			home = value
		}
		if key == "CLAUDE_CONFIG_DIR" && value != "" {
			return fail
		}
		if (key == "CLAUDE_CODE_OAUTH_TOKEN" || key == "ANTHROPIC_API_KEY") && value != "" {
			credential = true
		}
	}
	if home != job.Home || mode == "capture" && !credential || mode == "replay" && credential {
		return fail
	}
	stage = "read-input"
	raw, err := readClosed(input, 32768)
	if err != nil || !utf8.Valid(raw) || !uniqueSuiteKeys(raw) || safeCLIJSON(raw) != nil {
		return fail
	}
	if strings.Contains(string(raw), "${FALLBACK_HOME}") || strings.Contains(string(raw), "${FALLBACK_CWD}") {
		return fail
	}
	normalized := workerText(string(raw), job.Home, job.WorkDir, false)
	status := FallbackWorkerStatus{}
	nativePID := 0
	frames := []CLIFrame{}
	if mode == "replay" {
		file, e := os.Open(job.Path)
		if e != nil {
			return fail
		}
		tape, e := DecodeFallbackWorker(file)
		file.Close()
		if e != nil || !reflect.DeepEqual(tape.Args, job.Args) || tape.Input != normalized || ValidateFallbackProductionWait(tape.Status, job.ReplayWait) != nil {
			return fail
		}
		for _, frame := range tape.Frames {
			writer := output
			if frame.Stream == "stderr" {
				writer = diagnostic
			}
			_ = writer.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, e := io.WriteString(writer, workerText(frame.Data, job.Home, job.WorkDir, true)); e != nil {
				return fail
			}
		}
		status = tape.Status
		frames = tape.Frames
		// EOF is observable before the production wrapper completes. In particular,
		// an observer can drain stdout before its child is cancelled by the daemon.
		_ = output.Close()
		_ = diagnostic.Close()
		if status.Signal != 0 || job.ReplayWait != nil {
			waitBudget := min(35*time.Second, budget)
			deadline := time.Now().Add(waitBudget)
			for syscall.Kill(job.OwnerPID, 0) != syscall.ESRCH || syscall.Kill(job.GroupPID, 0) != syscall.ESRCH {
				if _, err := os.Lstat(job.Abort); err == nil {
					return fail
				}
				if time.Now().After(deadline) {
					return fail
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	} else {
		info, e := os.Stat(job.Claude)
		if !filepath.IsAbs(job.Claude) || e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fail
		}
		if fallbackWorkerDirectory(job.WorkDir) != nil {
			return fail
		}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		cmd := exec.CommandContext(ctx, job.Claude, job.Args...)
		cmd.Dir = job.WorkDir
		cmd.Env = env
		cmd.Stdin = strings.NewReader(string(raw))
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: job.GroupPID}
		cmd.WaitDelay = time.Second
		var mu sync.Mutex
		size := 0
		bad := false
		consume := func(stream string, reader io.Reader, writer *os.File) {
			r := bufio.NewReader(io.LimitReader(reader, MaxBytes+1))
			for {
				line, e := r.ReadString('\n')
				if line != "" {
					if strings.Contains(line, "${FALLBACK_HOME}") || strings.Contains(line, "${FALLBACK_CWD}") {
						mu.Lock()
						bad = true
						mu.Unlock()
						cancel()
						return
					}
					bound := workerText(line, job.Home, job.WorkDir, false)
					valid := utf8.ValidString(bound)
					if stream == "stdout" {
						valid = valid && safeCLIJSON([]byte(strings.TrimSpace(bound))) == nil
					} else {
						valid = valid && Safe([]byte(bound)) == nil
					}
					mu.Lock()
					size += len(bound)
					valid = valid && size <= MaxBytes && len(frames) < MaxFrames
					if valid {
						frames = append(frames, CLIFrame{stream, bound})
					} else {
						bad = true
					}
					mu.Unlock()
					if !valid {
						cancel()
						return
					}
					_ = writer.SetWriteDeadline(time.Now().Add(10 * time.Second))
					if _, err := io.WriteString(writer, line); err != nil {
						mu.Lock()
						bad = true
						mu.Unlock()
						cancel()
						return
					}
				}
				if e != nil {
					if e != io.EOF {
						mu.Lock()
						bad = true
						mu.Unlock()
						cancel()
					}
					return
				}
			}
		}
		stdout, e := cmd.StdoutPipe()
		if e != nil {
			return fail
		}
		stderr, e := cmd.StderrPipe()
		if e != nil {
			return fail
		}
		deadline, ok := ctx.Deadline()
		stdoutFile, stdoutOK := stdout.(*os.File)
		stderrFile, stderrOK := stderr.(*os.File)
		if !ok || !stdoutOK || !stderrOK || stdoutFile.SetReadDeadline(deadline) != nil || stderrFile.SetReadDeadline(deadline) != nil {
			return fail
		}
		stage = "native-start"
		if cmd.Start() != nil {
			return fail
		}
		nativePID = cmd.Process.Pid
		monitorDone := make(chan struct{})
		defer close(monitorDone)
		go func() {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-monitorDone:
					return
				case <-ticker.C:
					if _, err := os.Lstat(job.Abort); err == nil {
						cancel()
						return
					}
				}
			}
		}()
		var readers sync.WaitGroup
		readers.Add(2)
		go func() { defer readers.Done(); consume("stdout", stdout, output) }()
		go func() { defer readers.Done(); consume("stderr", stderr, diagnostic) }()
		stage = "native-output-eof"
		readers.Wait()
		stage = "native-wait"
		waitErr := cmd.Wait()
		env = nil
		if bad || ctx.Err() != nil || cmd.ProcessState == nil {
			return fail
		}
		status.ExitCode = cmd.ProcessState.ExitCode()
		nativeStatus, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ok {
			return fail
		}
		if nativeStatus.Signaled() {
			status.Signal = int(nativeStatus.Signal())
		}
		if waitErr != nil {
			var exit *exec.ExitError
			if !errors.As(waitErr, &exit) {
				return fail
			}
		}
		tape := &FallbackWorkerTape{DirectoryMode: 0700, DirectoryEmpty: true, Version: 1, Producer: job.Producer, Args: append([]string(nil), job.Args...), Input: normalized, InputEOF: true, Frames: frames, StdoutEOF: true, StderrEOF: true, Status: status}
		stage = "publish-tape"
		if tape.Save(job.Path) != nil {
			return fail
		}
	}
	stage = "publish-proof"
	tapeRaw, err := runnerPrivateRead(job.Path, MaxBytes)
	if err != nil {
		return fail
	}
	tapeHash := sha256.Sum256(tapeRaw)
	receipt := FallbackWorkerReceipt{TapeSHA256: hex.EncodeToString(tapeHash[:]), Version: 1, OwnerPID: job.OwnerPID, GroupPID: job.GroupPID, NativePID: nativePID, BrokerPID: os.Getpid(), Success: true, Frames: len(frames), Status: status}
	if PublishLifecycleControl(strings.TrimSuffix(job.Path, ".json")+".receipt", receipt) != nil {
		return fail
	}
	// This separate broker stdout is an outer-owned audit pipe, never native
	// provider stdout (fd4). Native code cannot forge this completion proof.
	if json.NewEncoder(os.Stdout).Encode(receipt) != nil {
		return fail
	}
	_ = output.Close()
	_ = diagnostic.Close()
	stage = "return-status"
	statusRaw, _ := json.Marshal(status)
	_ = statusPipe.SetWriteDeadline(time.Now().Add(time.Second))
	_, writeErr := statusPipe.Write(statusRaw)
	if writeErr != nil {
		if status.Signal != int(syscall.SIGKILL) && status != (FallbackWorkerStatus{ExitCode: 0}) {
			return fail
		}
		deadline := time.Now().Add(time.Second)
		for syscall.Kill(job.OwnerPID, 0) != syscall.ESRCH || syscall.Kill(job.GroupPID, 0) != syscall.ESRCH {
			if time.Now().After(deadline) {
				return fail
			}
			time.Sleep(time.Millisecond)
		}
	}
	return nil
}

// ConnectFallbackWorker transfers seven directed pipe capabilities using the
// same kernel-authenticated transport as stream brokers. Unlike stream Exec,
// its outer-owned broker starts and Waits the native print child in the actual
// production worker group. Group cancellation therefore kills native and helper
// together while the independent broker retains actual EOF and wait status.
func ConnectFallbackWorker(socket, job string, env []string) (FallbackWorkerStatus, error) {
	fail := errors.New("finite fallback connection failed")
	zero := FallbackWorkerStatus{}
	if !filepath.IsAbs(socket) || !filepath.IsAbs(job) || len(job) >= 4096 {
		return zero, fail
	}
	unusedR1, unusedW1, err := os.Pipe()
	if err != nil {
		return zero, fail
	}
	defer unusedR1.Close()
	defer unusedW1.Close()
	envR, envW, err := os.Pipe()
	if err != nil {
		return zero, fail
	}
	defer envR.Close()
	defer envW.Close()
	unusedR2, unusedW2, err := os.Pipe()
	if err != nil {
		return zero, fail
	}
	defer unusedR2.Close()
	defer unusedW2.Close()
	statusR, statusW, err := os.Pipe()
	if err != nil {
		return zero, fail
	}
	defer statusR.Close()
	defer statusW.Close()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return zero, fail
	}
	defer c.Close()
	files := []*os.File{os.Stdin, os.Stdout, os.Stderr, unusedW1, envR, unusedR2, statusW}
	fds := []int{}
	for _, f := range files {
		fds = append(fds, int(f.Fd()))
	}
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	rights := syscall.UnixRights(fds...)
	n, oob, e := c.WriteMsgUnix([]byte(job), rights, nil)
	if e != nil || n != len(job) || oob != len(rights) || c.CloseWrite() != nil {
		return zero, fail
	}
	// This API runs only inside the dedicated finite helper. The broker now owns
	// duplicated capabilities; retaining the helper's original stdout/stderr would
	// hide native EOF until wrapper exit and deadlock an observing parent at Wait.
	_ = os.Stdin.Close()
	_ = os.Stdout.Close()
	_ = os.Stderr.Close()
	for _, f := range []*os.File{unusedW1, envR, unusedR2, statusW} {
		_ = f.Close()
	}
	raw, e := json.Marshal(env)
	if e != nil || len(raw) > 1024*1024 {
		return zero, fail
	}
	_ = envW.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, e = envW.Write(raw); e != nil {
		return zero, fail
	}
	_ = envW.Close()
	raw = nil
	_ = statusR.SetReadDeadline(time.Now().Add(50 * time.Second))
	result, e := io.ReadAll(io.LimitReader(statusR, 16385))
	if e != nil || len(result) > 16384 {
		return zero, fail
	}
	var status FallbackWorkerStatus
	d := json.NewDecoder(bytes.NewReader(result))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(result) || d.Decode(&status) != nil || d.Decode(new(any)) != io.EOF {
		return zero, fail
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack [1]byte
	if _, e := io.ReadFull(c, ack[:]); e != nil || ack[0] != 1 {
		return zero, fail
	}
	return status, nil
}

func LifecycleWorkerLimit(scenario string) int {
	if PhysicalFamily(scenario) != "" || scenario == "fallback-suggestion" || scenario == "session-resume" || scenario == "conversation-system-prompt" || scenario == "conversation-active-switch" {
		return 2
	}
	return 0
}
func FallbackWorkerJobPath(home string, index int) string {
	return filepath.Join(home, "recording-workers", fmt.Sprintf("%06d.job", index))
}
func ValidateFallbackWorkerLaunch(job FallbackWorkerJob, path string, peer, parent int, base PoolProcessConfig, sourceSHA string) error {
	fail := errors.New("unowned finite fallback launch")
	validPath := false
	for i := 1; i <= LifecycleWorkerLimit(base.Scenario); i++ {
		validPath = validPath || path == FallbackWorkerJobPath(base.Home, i)
	}
	if !validPath || job.Version != 1 || job.OwnerPID != peer || peer <= 1 || !fallbackWorkerGroupShape(job) || job.BrokerParentPID != parent || job.Scenario != base.Scenario || job.Home != base.Home || job.Claude != base.Claude || job.Path != strings.TrimSuffix(path, ".job")+".json" || job.Abort != strings.TrimSuffix(path, ".job")+".abort" || job.SourceBinarySHA256 != sourceSHA || !reflect.DeepEqual(job.Producer, base.Producer) || fallbackWorkerArgs(job.Args) != nil || base.Mode == "capture" && job.ReplayWait != nil || ValidateFallbackProductionWait(FallbackWorkerStatus{ExitCode: 0}, job.ReplayWait) != nil {
		return fail
	}
	var daemon struct{ PID int }
	if ReadLifecycleControl(filepath.Join(base.Home, "recording-worker-daemon.json"), 16384, &daemon) != nil || daemon.PID <= 1 || job.DaemonPID != daemon.PID {
		return fail
	}
	if validateFallbackWorkerKernelGroup(job, peer) != nil {
		return fail
	}
	info, err := os.Lstat(job.WorkDir)
	if !filepath.IsAbs(job.WorkDir) || !strings.HasPrefix(filepath.Base(job.WorkDir), "pyry-reply-") || err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fail
	}
	if fallbackWorkerDirectory(job.WorkDir) != nil {
		return fail
	}
	return nil
}

type FallbackWorkerObservation struct {
	OwnerPID                                 int
	GroupCancel, WaitCompleted, ExitObserved bool
	Status                                   FallbackWorkerStatus
}
type LifecycleWorker struct {
	SourceRole     string                  `json:"source_role"`
	UserSequence   int                     `json:"user_sequence"`
	Tape           *FallbackWorkerTape     `json:"tape"`
	ProductionWait *FallbackProductionWait `json:"production_wait,omitempty"`
}

func ValidateLifecycleWorkers(scenario string, roles map[string]*LifecycleTape, workers []LifecycleWorker) error {
	fail := errors.New("unproved finite worker source dependency")
	if len(workers) > LifecycleWorkerLimit(scenario) {
		return fail
	}
	used := map[string]bool{}
	for _, worker := range workers {
		role := roles[worker.SourceRole]
		if role == nil || worker.Tape == nil || worker.Tape.Validate() != nil || ValidateFallbackProductionWait(worker.Tape.Status, worker.ProductionWait) != nil {
			return fail
		}
		sources, err := FallbackExchanges(role.Tape)
		if err != nil {
			return fail
		}
		source, ok := sources[worker.UserSequence]
		key := fmt.Sprintf("%s/%d", worker.SourceRole, worker.UserSequence)
		if !ok || used[key] || validateLifecycleWorkerSource(scenario, worker.SourceRole, worker.UserSequence, role.Tape, []byte(worker.Tape.Input), source) != nil {
			return fail
		}
		used[key] = true
	}
	return nil
}

// ReadLifecycleWorkers closes the complete private attempt inventory after the
// original daemon cleanup. Every attempt must have an actual broker receipt and
// matching production Wait/status observation; unknown or partial attempts fail.
func ReadLifecycleWorkers(home, scenario, mode, revision, sourceSHA string, parent int, roles map[string]*LifecycleTape) (workers []LifecycleWorker, resultErr error) {
	stage := "private-root"
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("finite worker inventory stage=%s", stage)
		}
	}()
	fail := errors.New("finite worker inventory changed")
	if mode != "capture" && mode != "replay" {
		return nil, fail
	}
	if LifecycleWorkerLimit(scenario) == 0 {
		return nil, nil
	}
	dir := filepath.Join(home, "recording-workers")
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fail
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fail
	}
	stage = "production-observations"
	var observations []FallbackWorkerObservation
	if ReadLifecycleControl(filepath.Join(home, "recording-worker-observations.json"), 16384, &observations) != nil {
		return nil, fail
	}
	stage = "original-daemon"
	var daemon struct{ PID int }
	if ReadLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), 16384, &daemon) != nil || daemon.PID <= 1 {
		return nil, fail
	}
	workers = []LifecycleWorker{}
	expected := map[string]bool{}
	owners := map[int]bool{}
	for i := 1; i <= LifecycleWorkerLimit(scenario); i++ {
		jobPath := FallbackWorkerJobPath(home, i)
		if _, err := os.Lstat(jobPath); os.IsNotExist(err) {
			break
		}
		stage = "exclusive-job"
		var job FallbackWorkerJob
		if ReadLifecycleControl(jobPath, 16384, &job) != nil || job.Version != 1 || job.OwnerPID <= 1 || owners[job.OwnerPID] || !fallbackWorkerGroupShape(job) || job.DaemonPID != daemon.PID || job.BrokerParentPID != parent || job.Scenario != scenario || job.Home != home || job.SourceBinarySHA256 != sourceSHA || job.Producer.Revision != revision || fallbackWorkerArgs(job.Args) != nil || job.Path != strings.TrimSuffix(jobPath, ".job")+".json" || job.Abort != strings.TrimSuffix(jobPath, ".job")+".abort" {
			return nil, fail
		}
		owners[job.OwnerPID] = true
		stage = "worker-carrier"
		file, err := os.Open(job.Path)
		if err != nil {
			return nil, fail
		}
		tape, err := DecodeFallbackWorker(file)
		file.Close()
		if err != nil || !reflect.DeepEqual(tape.Args, job.Args) {
			return nil, fail
		}
		stage = "worker-producer"
		if mode == "capture" && (!reflect.DeepEqual(tape.Producer, job.Producer) || job.Producer.BinarySHA256 != sourceSHA) {
			return nil, fail
		}
		stage = "native-receipt"
		var receipt FallbackWorkerReceipt
		if ReadLifecycleControl(strings.TrimSuffix(jobPath, ".job")+".receipt", 16384, &receipt) != nil || receipt.Version != 1 || !receipt.Success || receipt.OwnerPID != job.OwnerPID || receipt.GroupPID != job.GroupPID || receipt.BrokerPID <= 1 || receipt.BrokerPID == job.OwnerPID || receipt.Status != tape.Status || receipt.Frames != len(tape.Frames) || mode == "capture" && (receipt.NativePID <= 1 || receipt.NativePID == receipt.OwnerPID || receipt.NativePID == receipt.BrokerPID) || mode == "replay" && receipt.NativePID != 0 {
			return nil, fail
		}
		stage = "receipt-tape-digest"
		raw, err := runnerPrivateRead(job.Path, MaxBytes)
		if err != nil {
			return nil, fail
		}
		sum := sha256.Sum256(raw)
		if receipt.TapeSHA256 != hex.EncodeToString(sum[:]) {
			return nil, fail
		}

		stage = "actual-production-wait"
		worker := LifecycleWorker{Tape: tape}
		matches := 0
		for _, observation := range observations {
			if observation.OwnerPID == fallbackWorkerObservationPID(job) {
				matches++
				if !observation.WaitCompleted || !observation.ExitObserved || tape.Status.Signal != 0 && !observation.GroupCancel {
					return nil, fail
				}
				if observation.Status != tape.Status {
					worker.ProductionWait = &FallbackProductionWait{Status: observation.Status, GroupCancel: observation.GroupCancel}
					if ValidateFallbackProductionWait(tape.Status, worker.ProductionWait) != nil {
						return nil, fail
					}
				}
			}
		}
		stage = "wait-cardinality-and-death"
		if matches != 1 || syscall.Kill(job.OwnerPID, 0) != syscall.ESRCH || syscall.Kill(job.GroupPID, 0) != syscall.ESRCH {
			return nil, fail
		}
		if mode == "capture" && job.ReplayWait != nil || mode == "replay" && !reflect.DeepEqual(job.ReplayWait, worker.ProductionWait) {
			return nil, fail
		}
		stage = "source-derivation"
		sourceMatches := 0
		for role, provider := range roles {
			sources, err := FallbackExchanges(provider.Tape)
			if err != nil {
				return nil, fail
			}
			for seq, source := range sources {
				if validateLifecycleWorkerSource(scenario, role, seq, provider.Tape, []byte(tape.Input), source) == nil {
					sourceMatches++
					worker.SourceRole = role
					worker.UserSequence = seq
				}
			}
		}
		stage = "unique-completed-source"
		if sourceMatches != 1 {
			return nil, fail
		}
		workers = append(workers, worker)
		for _, suffix := range []string{".job", ".json", ".receipt"} {
			expected[fmt.Sprintf("%06d%s", i, suffix)] = true
		}
	}
	stage = "total-wait-cardinality"
	if len(observations) != len(workers) {
		return nil, fail
	}
	stage = "closed-private-inventory"
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return nil, fail
		}
	}
	if len(entries) != len(expected) || ValidateLifecycleWorkers(scenario, roles, workers) != nil {
		return nil, fail
	}
	return workers, nil
}
func SameLifecycleWorkers(a, b *LifecycleSuite) error {
	fail := errors.New("finite fallback outgoing contract changed")
	if len(a.Workers) != len(b.Workers) {
		return fail
	}
	for i, left := range a.Workers {
		right := b.Workers[i]
		if left.SourceRole != right.SourceRole || left.UserSequence != right.UserSequence || left.Tape.Status != right.Tape.Status || !reflect.DeepEqual(left.ProductionWait, right.ProductionWait) || !reflect.DeepEqual(left.Tape.Args, right.Tape.Args) {
			return fail
		}
		// Both complete literal edges have already validated against their own
		// native output. Only assistant output may vary across fresh captures.
		var x, y FallbackExchange
		if json.Unmarshal([]byte(left.Tape.Input), &x) != nil || json.Unmarshal([]byte(right.Tape.Input), &y) != nil || x.User != y.User {
			return fail
		}
	}
	return nil
}

func fallbackWorkerDirectory(path string) error {
	fail := errors.New("finite worker cwd changed")
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode().Perm() != 0700 {
		return fail
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail
	}
	dir := os.NewFile(uintptr(fd), "finite-worker-cwd")
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return fail
	}
	entries, err := dir.ReadDir(1)
	if err != io.EOF || len(entries) != 0 {
		return fail
	}
	return nil
}

// ValidateFallbackWorkerCompletion binds the private receipt and tape bytes to
// the actual broker's separate stdout capability retained by the outer parent.
// The expected PID is from that parent's exec.Cmd, never a fixture or job claim.
func ValidateFallbackWorkerCompletion(raw []byte, brokerPID int, path string) error {
	fail := errors.New("finite worker completion proof changed")
	if len(raw) > 16384 || brokerPID <= 1 || !uniqueSuiteKeys(raw) {
		return fail
	}
	var receipt FallbackWorkerReceipt
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF || receipt.Version != 1 || !receipt.Success || receipt.BrokerPID != brokerPID {
		return fail
	}
	stored, err := runnerPrivateRead(strings.TrimSuffix(path, ".job")+".receipt", 16384)
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), stored) {
		return fail
	}
	tape, err := runnerPrivateRead(strings.TrimSuffix(path, ".job")+".json", MaxBytes)
	if err != nil {
		return fail
	}
	sum := sha256.Sum256(tape)
	if receipt.TapeSHA256 != hex.EncodeToString(sum[:]) {
		return fail
	}
	return nil
}

// ValidateFallbackReplayWait binds the replay-only cancellation plan to the
// exact saved attempt index held by the outer parent, independently of the job.
func ValidateFallbackReplayWait(job FallbackWorkerJob, path, mode, home string, waits []*FallbackProductionWait) error {
	fail := errors.New("finite replay wait plan changed")
	if mode == "capture" {
		if job.ReplayWait != nil || len(waits) != 0 {
			return fail
		}
		return nil
	}
	if mode != "replay" || len(waits) > LifecycleWorkerLimit(job.Scenario) {
		return fail
	}
	for i, wait := range waits {
		if path == FallbackWorkerJobPath(home, i+1) {
			if ValidateFallbackProductionWait(FallbackWorkerStatus{ExitCode: 0}, wait) != nil || !reflect.DeepEqual(job.ReplayWait, wait) {
				return fail
			}
			return nil
		}
	}
	return fail
}
