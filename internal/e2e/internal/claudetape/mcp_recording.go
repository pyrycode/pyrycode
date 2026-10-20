package claudetape

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

func MCPScenario(scenario string) bool { return scenario == "mcp-send-file" }

type MCPRecordingProof struct {
	Version                                                    int
	Mode, Scenario, Revision, SourceBinarySHA256, DaemonSHA256 string
	OwnerPID, BrokerPID                                        int
	Binding                                                    LifecycleRoleBinding
	SourceTapeSHA256, SourceExchangeSHA256                     string
	StartedAt, SealedAt                                        string
	Actual                                                     MCPExchange
}
type MCPRecordingTerminal struct {
	Prefix  MCPRecordingProof
	Servers []e2emcp.ServerProof
}
type MCPRecordingSession struct {
	privateValues             []string
	idReady                   chan struct{}
	idOnce                    sync.Once
	config                    LifecycleBrokerConfig
	binding                   LifecycleRoleBinding
	source, daemonSHA, prefix string
	broker                    int
	ctx                       context.Context
	cancel                    context.CancelFunc
	stop                      chan struct{}
	watch                     sync.WaitGroup
	coordinator               *e2emcp.Coordinator
	proxy                     *e2emcp.ProxyServer
	replay                    *e2emcp.ReplayServers
	expected                  *MCPExchange
	facts                     MCPSendFileFacts
	mu                        sync.Mutex
	actualID                  string
	started                   time.Time
	sealed                    *MCPRecordingProof
	outputs                   int
	outputIndexes             []int
	closeOnce                 sync.Once
}

func mcpHash(value any) (string, error) {
	raw, e := json.Marshal(value)
	if e != nil || len(raw) > MaxBytes || safeCLIJSON(raw) != nil {
		return "", mcpError()
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func publishMCP(path string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil || len(raw) > MaxBytes || safeCLIJSON(raw) != nil {
		return mcpError()
	}
	return publishClosedFile(filepath.Dir(path), filepath.Base(path), raw)
}
func ReadMCPExchange(path string) (*MCPExchange, error) {
	var exchange MCPExchange
	if ReadLifecycleControl(path, MaxBytes, &exchange) != nil {
		return nil, mcpError()
	}
	return &exchange, nil
}
func PublishMCPExchange(path string, x *MCPExchange, tape *Tape) error {
	if _, e := ValidateMCPSendFile(x, tape); e != nil {
		return e
	}
	return publishMCP(path, x)
}

// NewMCPRecordingSession is usable only by the closed send-file original root.
// All private current role/config/source identities are supplied by its actual
// lifecycle broker. No recorded PID or environment can launch a process here.
func NewMCPRecordingSession(config LifecycleBrokerConfig, binding LifecycleRoleBinding, b *DaemonBinding, source, daemonSHA string, broker int) (*MCPRecordingSession, error) {
	if config.Scenario != "mcp-send-file-bootstrap" || config.End != "seal" || binding.Role != "bootstrap" || binding.SessionID != "77777777-7777-4777-8777-777777777777" || binding.ConversationID != "55555555-5555-4555-8555-555555555555" || binding.WorkDir != filepath.Join(config.Home, "work") || b == nil || b.home != config.Home || config.WorkDir != binding.WorkDir || config.Path != filepath.Join(config.Home, "recording-children", "bootstrap.json") || config.Claim != filepath.Join(config.Home, "recording-children", "bootstrap.claim") || config.Finish != filepath.Join(config.Home, "finish") || config.Abort != filepath.Join(config.Home, "recording-children", "bootstrap.abort") || b.session != binding.SessionID || b.workdir != binding.WorkDir || broker != os.Getpid() || broker <= 1 || config.OwnerPID <= 1 || broker == config.OwnerPID || !physicalSHA256.MatchString(source) || !physicalSHA256.MatchString(daemonSHA) || len(config.Producer.Revision) != 40 || (config.Mode != "capture" && config.Mode != "replay") || ValidateMCPDaemonInput(b) != nil {
		return nil, mcpError()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	s := &MCPRecordingSession{config: config, binding: binding, source: source, daemonSHA: daemonSHA, prefix: strings.TrimSuffix(config.Path, ".json"), broker: broker, ctx: ctx, cancel: cancel, stop: make(chan struct{}), idReady: make(chan struct{}), started: time.Now()}
	launch := e2emcp.LaunchBinding{NativeOwnerPID: config.OwnerPID, Binary: b.daemon, BinarySHA256: daemonSHA, Home: config.Home, WorkDir: binding.WorkDir, Socket: b.socket, SessionID: binding.SessionID}
	var e error
	if config.Mode == "capture" {
		s.coordinator, e = e2emcp.NewCoordinator(func(direction e2emcp.Direction, raw []byte) error {
			if s.privateSafe(raw) != nil {
				return mcpError()
			}
			return validateMCPFrame(direction, raw)
		})
		if e == nil {
			s.proxy, e = e2emcp.NewProxyServer(ctx, launch, s.coordinator, s.privateSafe)
			if e == nil {
				socket, token := s.proxy.Capability()
				s.privateValues = []string{socket, token}
			}
		}
	} else {
		s.expected, e = ReadMCPExchange(s.prefix + ".mcp-input")
		var tape *LifecycleTape
		if e == nil {
			raw, err := runnerPrivateRead(config.Path, MaxBytes)
			if err != nil {
				e = err
			} else {
				tape, e = DecodeLifecycle(bytes.NewReader(raw))
			}
		}
		if e == nil {
			files, err := b.Files()
			if err != nil || !reflect.DeepEqual(b.Args(), tape.Tape.Args) || tape.Tape.CheckFiles(files) != nil {
				e = mcpError()
			} else {
				s.facts, e = ValidateMCPSendFile(s.expected, tape.Tape)
			}
			for index, frame := range tape.Tape.Frames {
				if frame.Direction == "stdout" {
					s.outputIndexes = append(s.outputIndexes, index)
				}
			}
		}
		if e == nil {
			s.coordinator, e = e2emcp.NewReplayCoordinator(ctx, s.expected.Events, validateMCPFrame, func(expected e2emcp.RPCEvent, actual []byte) error {
				id, err := MatchMCPSendFileResponse(expected, actual, s.facts, config.Home, binding.ConversationID)
				if err == nil && id != "" {
					if setErr := s.setCurrentID(id); setErr != nil {
						return setErr
					}
				}
				return err
			})
		}
		if e == nil {
			env := []string{}
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				switch name {
				case "HOME", "PYRY_SESSION_ID", e2emcp.SocketEnv, e2emcp.TokenEnv, "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR":
					continue
				}
				env = append(env, entry)
			}
			env = append(env, "HOME="+config.Home, "PYRY_SESSION_ID="+binding.SessionID)
			s.replay, e = e2emcp.StartReplayServers(ctx, launch, s.coordinator, []string{"mcp-approve", "mcp-files"}, env, func(_ string, raw []byte) error { return Safe(raw) })
		}
	}
	if e != nil {
		cancel()
		return nil, e
	}
	// This independent watcher can interrupt scheduler waits even when the main
	// single-pump broker cannot get back to reading its ordinary Abort file.
	s.watch.Add(1)
	go func() {
		defer s.watch.Done()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-ticker.C:
				if _, err := os.Lstat(config.Abort); err == nil || !os.IsNotExist(err) {
					cancel()
					return
				}
			}
		}
	}()
	return s, nil
}
func (s *MCPRecordingSession) Configure(c *LifecycleBrokerConfig) error {
	if c == nil || c.Context != nil || c.Ready != nil || c.ForwardInput != nil || c.ForwardOutput != nil || c.BeforeSeal != nil || c.ReplayOutput != nil {
		return mcpError()
	}
	c.Context = s.ctx
	c.Ready = func(w io.Writer) error {
		capability := e2emcp.Capability{Version: 1, Mode: s.config.Mode}
		if s.proxy != nil {
			capability.Socket, capability.Token = s.proxy.Capability()
		}
		return e2emcp.WriteCapability(w, capability)
	}
	c.ForwardInput = func(raw []byte, write func() error) error {
		if s.privateSafe(raw) != nil {
			return mcpError()
		}
		return s.coordinator.NativeInput(lifecycleTimelineUser(raw), write)
	}
	c.ForwardOutput = func(raw []byte, write func() error) error {
		if s.privateSafe(raw) != nil {
			return mcpError()
		}
		return s.coordinator.NativeOutput(write)
	}
	if s.config.Mode == "replay" {
		c.ReplayOutput = func(raw []byte) ([]byte, error) {
			if s.outputs >= len(s.outputIndexes) {
				return nil, mcpError()
			}
			index := s.outputIndexes[s.outputs]
			s.outputs++
			if len(s.facts.NativeBindings[index]) > 0 {
				select {
				case <-s.idReady:
				case <-s.ctx.Done():
					return nil, mcpError()
				}
			}
			s.mu.Lock()
			id := s.actualID
			s.mu.Unlock()
			return ReplayMCPSendFileOutput(raw, index, s.facts, id)
		}
	}
	c.BeforeSeal = s.Seal
	return nil
}
func (s *MCPRecordingSession) stderr() (map[string]string, error) {
	if s.proxy != nil {
		return s.proxy.StderrSnapshot()
	}
	return s.replay.StderrSnapshot()
}
func (s *MCPRecordingSession) Seal(tape *Tape) error {
	if s.sealed != nil || s.ctx.Err() != nil {
		return mcpError()
	}
	events, e := s.coordinator.Checkpoint()
	if e != nil {
		return e
	}
	var logs map[string]string
	deadline := time.Now().Add(5 * time.Second)
	for {
		logs, e = s.stderr()
		if e != nil {
			return e
		}
		complete := len(logs) == 2
		for _, kind := range []string{"mcp-files", "mcp-approve"} {
			body := logs[kind]
			lines := strings.Count(body, "\n")
			if lines > 2 || s.privateSafe([]byte(body)) != nil {
				return mcpError()
			}
			complete = complete && lines == 2 && strings.HasSuffix(body, "\n")
		}
		if complete {
			break
		}
		if s.ctx.Err() != nil || !time.Now().Before(deadline) {
			return mcpError()
		}
		time.Sleep(time.Millisecond)
	}
	actual := MCPExchange{Version: 1, Events: events, Stderr: logs}
	source := s.expected
	if s.config.Mode == "capture" {
		s.facts, e = ValidateMCPSendFile(&actual, tape)
		if e != nil {
			return e
		}
		source = &actual
		if s.setCurrentID(s.facts.AttachmentID) != nil {
			return mcpError()
		}
	} else {
		current, e := mcpCurrentValidationTape(tape, s.facts, s.currentID(), s.started)
		if e != nil {
			return e
		}
		if _, e = ValidateMCPSendFile(&actual, current); e != nil {
			return e
		}
	}
	if ValidateMCPSendFileEffect(s.config.Home, s.binding.ConversationID, s.currentID()) != nil {
		return mcpError()
	}
	tapeSHA, e := mcpHash(tape)
	if e != nil {
		return e
	}
	exchangeSHA, e := mcpHash(source)
	if e != nil {
		return e
	}
	proof := MCPRecordingProof{Version: 1, Mode: s.config.Mode, Scenario: s.config.Scenario, Revision: s.config.Producer.Revision, SourceBinarySHA256: s.source, DaemonSHA256: s.daemonSHA, OwnerPID: s.config.OwnerPID, BrokerPID: s.broker, Binding: s.binding, SourceTapeSHA256: tapeSHA, SourceExchangeSHA256: exchangeSHA, StartedAt: s.started.UTC().Format(time.RFC3339Nano), SealedAt: time.Now().UTC().Format(time.RFC3339Nano), Actual: actual}
	if s.ctx.Err() != nil || publishMCP(s.prefix+".mcp-prefix", proof) != nil {
		return mcpError()
	}
	s.sealed = &proof
	return nil
}
func (s *MCPRecordingSession) currentID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.actualID }
func mcpCurrentValidationTape(source *Tape, facts MCPSendFileFacts, id string, started time.Time) (*Tape, error) {
	if source == nil {
		return nil, mcpError()
	}
	current := *source
	current.Producer = source.Producer
	// An ephemeral validation view of actual replay effects/log times, never a
	// provider recording or replacement of the immutable source producer bytes.
	current.Producer.CapturedAt = started.UTC().Format(time.RFC3339Nano)
	current.Frames = append([]Frame(nil), source.Frames...)
	for index := range facts.NativeBindings {
		if index < 0 || index >= len(current.Frames) {
			return nil, mcpError()
		}
		raw, e := ReplayMCPSendFileOutput(current.Frames[index].Data, index, facts, id)
		if e != nil {
			return nil, e
		}
		current.Frames[index].Data = raw
	}
	return &current, nil
}

// Close(true) is called only after RunLifecycleBroker reports genuine primary
// terminal EOF after the original assertions. Prefix seal alone cannot close
// replay client stdin or manufacture a successful process completion.
func (s *MCPRecordingSession) Close(primarySucceeded bool) error {
	var result error
	called := false
	s.closeOnce.Do(func() {
		called = true
		defer close(s.stop)
		defer s.watch.Wait()
		defer s.cancel()
		if !primarySucceeded || s.sealed == nil || s.ctx.Err() != nil {
			s.cancel()
			if s.proxy != nil {
				_, _ = s.proxy.Join(5 * time.Second)
			} else {
				s.replay.Abort()
			}
			s.coordinator.Abort()
			result = mcpError()
			return
		}
		var proofs []e2emcp.ServerProof
		var e error
		if s.proxy != nil {
			proofs, e = s.proxy.Join(10 * time.Second)
		} else {
			proofs, e = s.replay.CompleteAndJoin(10 * time.Second)
		}
		if e != nil {
			result = e
			return
		}
		events, e := s.coordinator.Finish()
		if e != nil || !reflect.DeepEqual(events, s.sealed.Actual.Events) {
			result = mcpError()
			return
		}
		logs, e := s.stderr()
		if e != nil || !reflect.DeepEqual(logs, s.sealed.Actual.Stderr) || s.ctx.Err() != nil {
			result = mcpError()
			return
		}
		terminal := MCPRecordingTerminal{Prefix: *s.sealed, Servers: proofs}
		if validateMCPServerProofs(terminal) != nil {
			result = mcpError()
			return
		}
		result = publishMCP(s.prefix+".mcp", terminal)
	})
	if !called {
		return mcpError()
	}
	return result
}
func validateMCPServerProofs(terminal MCPRecordingTerminal) error {
	prefix := terminal.Prefix
	if len(terminal.Servers) != 2 {
		return mcpError()
	}
	seen := map[string]bool{}
	pids := map[int]bool{prefix.OwnerPID: true, prefix.BrokerPID: true}
	for _, server := range terminal.Servers {
		if seen[server.Kind] || (server.Kind != "mcp-files" && server.Kind != "mcp-approve") || server.NativeOwnerPID != prefix.OwnerPID || server.ServerPID <= 1 || server.ServerPID == prefix.OwnerPID || server.ServerPID == prefix.BrokerPID || !server.InputEOF || !server.OutputEOF || !server.StderrEOF || server.ExitCode != 0 || server.Stderr != prefix.Actual.Stderr[server.Kind] {
			return mcpError()
		}
		if prefix.Mode == "capture" && (server.NativeProxyPID <= 1 || server.NativeProxyPID == prefix.OwnerPID || server.NativeProxyPID == server.ServerPID || server.NativeProxyPID == prefix.BrokerPID) || prefix.Mode == "replay" && server.NativeProxyPID != 0 {
			return mcpError()
		}
		if pids[server.ServerPID] {
			return mcpError()
		}
		pids[server.ServerPID] = true
		if prefix.Mode == "capture" {
			if pids[server.NativeProxyPID] {
				return mcpError()
			}
			pids[server.NativeProxyPID] = true
		}
		inputs, outputs := 0, 0
		for _, event := range prefix.Actual.Events {
			if event.Connection == server.Kind {
				if event.Direction == e2emcp.ClientToServer {
					inputs++
				} else if event.Direction == e2emcp.ServerToClient {
					outputs++
				} else {
					return mcpError()
				}
			}
		}
		if inputs != server.InputFrames || outputs != server.OutputFrames {
			return mcpError()
		}
		seen[server.Kind] = true
	}
	if terminal.Servers[0].ServerPID == terminal.Servers[1].ServerPID {
		return mcpError()
	}
	return nil
}

func (s *MCPRecordingSession) setCurrentID(id string) error {
	if !poolUUID.MatchString(id) || s.ctx.Err() != nil {
		return mcpError()
	}
	s.mu.Lock()
	if s.actualID != "" {
		s.mu.Unlock()
		return mcpError()
	}
	s.actualID = id
	s.mu.Unlock()
	s.idOnce.Do(func() { close(s.idReady) })
	return nil
}

func (s *MCPRecordingSession) privateSafe(raw []byte) error {
	return mcpPrivateSafe(raw, s.privateValues...)
}
