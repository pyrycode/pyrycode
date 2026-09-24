// Package codexsup drives `codex app-server` (target: Codex 0.156.1) over its
// stdio JSON-RPC connection: it starts the process, performs the handshake,
// and starts, resumes and interrupts turns on one Codex thread. Server
// notifications reach the caller raw; server requests (approval prompts and
// the rest) reach a caller handler, or are declined by default — no default
// path ever accepts.
//
// The wire contract is codex_app_server_protocol.schemas.json (see
// SCHEMA.md). Framing is internal/acp's Transport, reused unchanged.
//
// Params are never logged: they carry prompt text, commands and file contents.
package codexsup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/acp"
)

// clientName is the initialize request's clientInfo.name.
const clientName = "pyrycode"

// killGrace is how long Stop's SIGTERM, or a descendant holding the child's
// stdout open, may delay exit before os/exec escalates to SIGKILL.
const killGrace = 5 * time.Second

// ErrExited is wrapped by every call that fails because the app-server
// process is gone, whether the caller stopped it or it died on its own.
var ErrExited = errors.New("codexsup: app-server exited")

// ErrNoThread is returned by StartTurn and Interrupt before a thread was
// started or resumed.
var ErrNoThread = errors.New("codexsup: no thread")

// Config configures one app-server process.
type Config struct {
	// Binary is the codex executable; Dir its working directory; CodexHome
	// its CODEX_HOME. All three are required.
	Binary    string
	Dir       string
	CodexHome string
	// ClientVersion is sent as clientInfo.version; empty sends "dev".
	ClientVersion string

	// OnNotification receives every server notification, raw and in arrival
	// order. It runs on the connection's read loop: it must not block, and
	// must not call Client methods (their responses are read by that loop).
	// params is untrusted input.
	OnNotification func(method string, params json.RawMessage)
	// OnServerRequest receives every server request under the same read-loop
	// rules. It must answer each request exactly once, now or later from any
	// goroutine. Nil declines every request (see ServerRequest.Decline).
	OnServerRequest func(req *ServerRequest)

	// Stderr receives the process's stderr; nil discards it.
	Stderr io.Writer
	// Log receives diagnostics; nil uses slog.Default().
	Log *slog.Logger
}

// TurnInput is one turn's text input and its per-turn overrides. Empty Model
// or Effort leaves the thread's setting unchanged.
type TurnInput struct {
	Text   string
	Model  string
	Effort string
}

// Client is a connection to one running app-server process.
type Client struct {
	t     *acp.Transport
	stdin io.WriteCloser
	kill  func()
	cmd   *exec.Cmd // nil for an in-memory peer
	dir   string

	clientVersion string

	// exitCtx is cancelled once the connection has drained after exit;
	// calls in flight then fail instead of hanging.
	exitCtx    context.Context
	cancelExit context.CancelFunc
	done       chan struct{}
	err        error // written before done closes
	stopOnce   sync.Once

	userAgent, version string // set by handshake before Start returns

	mu       sync.Mutex
	threadID string
}

// Start launches `<Binary> app-server` and performs the handshake: initialize,
// then the initialized notification. ctx bounds the handshake only; the
// process runs until Stop or its own exit.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Binary == "" || cfg.Dir == "" || cfg.CodexHome == "" {
		return nil, errors.New("codexsup: Binary, Dir and CodexHome are required")
	}
	procCtx, cancelProc := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, cfg.Binary, "app-server")
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(), "CODEX_HOME="+cfg.CodexHome)
	cmd.Stderr = cfg.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = killGrace
	// Stdout goes through a pipe the wait goroutine closes after Wait, so the
	// transport reaches EOF only once every byte the child wrote is delivered.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancelProc()
		return nil, fmt.Errorf("codexsup: stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancelProc()
		return nil, fmt.Errorf("codexsup: start: %w", err)
	}
	waitErr := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		waitErr <- err
	}()
	kill := func() {
		cancelProc()
		// Unblocks os/exec's stdout copier if the transport stopped reading.
		_ = pr.CloseWithError(ErrExited)
	}
	wait := func() error {
		err := <-waitErr
		cancelProc()
		return err
	}
	c := newClient(cfg, pr, stdin, wait, kill)
	c.cmd = cmd
	if err := c.handshake(ctx); err != nil {
		kill()
		<-c.done
		return nil, err
	}
	return c, nil
}

// newClient runs the connection over r (from the server) and w (to it). wait
// blocks until the server is gone and returns its exit error; kill forces it
// gone. Every server notification and request method is registered here,
// before Serve starts.
func newClient(cfg Config, r io.Reader, w io.WriteCloser, wait func() error, kill func()) *Client {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	t := acp.New(r, w, log)
	for _, m := range serverNotifications {
		t.Register(m, func(_ context.Context, params json.RawMessage) (any, error) {
			if cfg.OnNotification != nil {
				cfg.OnNotification(m, params)
			}
			return nil, nil
		})
	}
	for _, m := range serverRequests {
		t.Register(m, serverRequestHandler(m, cfg.OnServerRequest))
	}
	exitCtx, cancelExit := context.WithCancel(context.Background())
	clientVersion := cfg.ClientVersion
	if clientVersion == "" {
		clientVersion = "dev"
	}
	c := &Client{
		clientVersion: clientVersion,
		t:             t,
		stdin:         w,
		kill:          kill,
		dir:           cfg.Dir,
		exitCtx:       exitCtx,
		cancelExit:    cancelExit,
		done:          make(chan struct{}),
	}
	go c.run(wait, kill)
	return c
}

// run reads the connection until the server's output ends, then records the
// exit and fails every call still waiting.
func (c *Client) run(wait func() error, kill func()) {
	serveErr := c.t.Serve(context.Background())
	if serveErr != nil {
		// A broken stream (e.g. an over-long line): nothing reads the child
		// any more, so stop it.
		kill()
	}
	c.err = errors.Join(serveErr, wait())
	c.cancelExit()
	close(c.done)
}

func (c *Client) handshake(ctx context.Context) error {
	params := struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}{}
	params.ClientInfo.Name, params.ClientInfo.Title = clientName, "Pyrycode"
	params.ClientInfo.Version = c.clientVersion
	var res struct {
		UserAgent string `json:"userAgent"`
	}
	if err := c.call(ctx, methodInitialize, params, &res); err != nil {
		return err
	}
	c.userAgent, c.version = res.UserAgent, parseVersion(res.UserAgent)
	if err := c.t.Notify(methodInitialized, nil); err != nil {
		return fmt.Errorf("codexsup: %s: %w", methodInitialized, err)
	}
	return nil
}

// parseVersion takes the version from a userAgent shaped
// `<originator>/<version> (<platform>) …`; it is empty when there is no "/".
func parseVersion(userAgent string) string {
	_, rest, ok := strings.Cut(userAgent, "/")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, " ("); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// Version is the Codex version from the initialize response's userAgent.
func (c *Client) Version() string { return c.version }

// UserAgent is the initialize response's userAgent, unparsed.
func (c *Client) UserAgent() string { return c.userAgent }

// Done is closed once the process has exited and its output has been read.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is the process's exit error once Done is closed (nil on a clean exit),
// and nil before.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// Stop closes the process's stdin, on which app-server exits, and waits for
// the exit. If ctx ends first the process is sent SIGTERM, then SIGKILL after
// killGrace. It returns Err.
func (c *Client) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() { _ = c.stdin.Close() })
	select {
	case <-c.done:
	case <-ctx.Done():
		c.kill()
		<-c.done
	}
	return c.Err()
}

// exitError wraps ErrExited with the process's exit error.
func (c *Client) exitError() error {
	<-c.done
	if c.err != nil {
		return fmt.Errorf("%w: %w", ErrExited, c.err)
	}
	return ErrExited
}

// call sends one request and decodes its result into out (skipped when nil).
// It fails with ErrExited when the process exits before answering.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.exitCtx, cancel)
	defer stop()
	raw, err := c.t.Call(ctx, method, params)
	if err != nil {
		if c.exitCtx.Err() != nil {
			return fmt.Errorf("codexsup: %s: %w", method, c.exitError())
		}
		return fmt.Errorf("codexsup: %s: %w", method, err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("codexsup: %s: decode result: %w", method, err)
		}
	}
	return nil
}

type threadResult struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

// setThread records the thread id a thread/start or thread/resume returned.
func (c *Client) setThread(method string, res threadResult) (string, error) {
	if res.Thread.ID == "" {
		return "", fmt.Errorf("codexsup: %s: result has no thread id", method)
	}
	c.mu.Lock()
	c.threadID = res.Thread.ID
	c.mu.Unlock()
	return res.Thread.ID, nil
}

// StartThread starts a thread in the configured directory and returns the id
// Codex minted, which becomes the client's thread.
func (c *Client) StartThread(ctx context.Context) (string, error) {
	params := struct {
		Cwd string `json:"cwd,omitempty"`
	}{c.dir}
	var res threadResult
	if err := c.call(ctx, methodThreadStart, params, &res); err != nil {
		return "", err
	}
	return c.setThread(methodThreadStart, res)
}

// ResumeThread resumes the thread with id, without its turn history, and
// makes it the client's thread.
func (c *Client) ResumeThread(ctx context.Context, threadID string) error {
	params := struct {
		ThreadID     string `json:"threadId"`
		Cwd          string `json:"cwd,omitempty"`
		ExcludeTurns bool   `json:"excludeTurns"`
	}{threadID, c.dir, true}
	var res threadResult
	if err := c.call(ctx, methodThreadResume, params, &res); err != nil {
		return err
	}
	_, err := c.setThread(methodThreadResume, res)
	return err
}

// ThreadID is the client's current thread, empty before one was started or
// resumed.
func (c *Client) ThreadID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.threadID
}

func (c *Client) requireThread() (string, error) {
	id := c.ThreadID()
	if id == "" {
		return "", ErrNoThread
	}
	return id, nil
}

// StartTurn starts a turn on the client's thread and returns its id. The
// turn's progress arrives through OnNotification.
func (c *Client) StartTurn(ctx context.Context, in TurnInput) (string, error) {
	threadID, err := c.requireThread()
	if err != nil {
		return "", err
	}
	type textInput struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	params := struct {
		ThreadID string      `json:"threadId"`
		Input    []textInput `json:"input"`
		Model    string      `json:"model,omitempty"`
		Effort   string      `json:"effort,omitempty"`
	}{threadID, []textInput{{"text", in.Text}}, in.Model, in.Effort}
	var res struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := c.call(ctx, methodTurnStart, params, &res); err != nil {
		return "", err
	}
	if res.Turn.ID == "" {
		return "", fmt.Errorf("codexsup: %s: result has no turn id", methodTurnStart)
	}
	return res.Turn.ID, nil
}

// Interrupt asks Codex to stop the running turn turnID on the client's
// thread. The turn ends with a turn/completed notification.
func (c *Client) Interrupt(ctx context.Context, turnID string) error {
	threadID, err := c.requireThread()
	if err != nil {
		return err
	}
	params := struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}{threadID, turnID}
	return c.call(ctx, methodTurnInterrupt, params, nil)
}
