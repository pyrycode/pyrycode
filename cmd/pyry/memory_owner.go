package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/memoryruntime"
	"golang.org/x/sys/unix"
)

//go:embed memory_owner.py
var memoryOwnerShim string

var (
	errMemoryOccupied    = errors.New("memory owner: store occupied; retry after release")
	errMemoryOwner       = errors.New("memory owner: initialization failed; retry is available")
	errMemoryRelease     = errors.New("memory owner: cleanup incomplete; exclusion retained by surviving owner")
	errMemoryDestination = errors.New("memory owner: unsafe mutable destination")
)

const memoryOwnerJoin = 8 * time.Second

type memoryOwner struct {
	channel net.Conn
	done    chan struct{}
	once    sync.Once
	err     error // published by closing done
}

type memoryOwnerOutput struct {
	bytes atomic.Int64
	stop  func()
}

func (o *memoryOwnerOutput) Write(data []byte) (int, error) {
	if o.bytes.Add(int64(len(data))) > 64<<10 {
		o.stop()
	}
	return len(data), nil
}

type memoryOwnerOptions struct {
	shim       string
	credential func(context.Context, string) (string, error)
	endpoint   string // test-only; never populated by startMemoryOwner
}

type memoryOwnerRequest struct {
	Store      string            `json:"store"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	Credential string            `json:"credential"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Versions   map[string]string `json:"versions"`
}

// startMemoryOwner establishes provider/database health, without index or agent readiness.
// The caller retains this owner for later operations and closes it before shutdown.
func startMemoryOwner(ctx context.Context, rt memoryruntime.Runtime, s config.MemorySettings, e effectiveMemory) (*memoryOwner, error) {
	return startMemoryOwnerWith(ctx, rt, s, e, memoryOwnerOptions{})
}
func startMemoryOwnerWith(ctx context.Context, rt memoryruntime.Runtime, s config.MemorySettings, e effectiveMemory, options memoryOwnerOptions) (*memoryOwner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rt.Python == "" || rt.Versions["memsearch"] != "0.4.21" || rt.Versions["milvus-lite"] == "" || s.Embedding.Model == "" || (s.Embedding.Provider != "local" && s.Embedding.Provider != "openai") {
		return nil, errMemoryOwner
	}
	home, err := memoryHome()
	if err != nil {
		return nil, errMemoryDestination
	}
	store, lock, err := memoryOwnerStorage(ctx, home, e)
	if err != nil {
		return nil, err
	}
	defer lock.Close() // child inherits the same locked open-file description
	request := memoryOwnerRequest{Store: store, Provider: s.Embedding.Provider, Model: s.Embedding.Model, Versions: rt.Versions, Endpoint: options.endpoint}
	if request.Provider == "openai" {
		resolve := options.credential
		if resolve == nil {
			resolve = resolveMemoryCredential
		}
		request.Credential, err = resolve(ctx, s.Embedding.CredentialReference)
		if err != nil {
			return nil, errMemorySelection
		}
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) > 64<<10 {
		return nil, errMemoryOwner
	}
	syscall.ForkLock.RLock()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(pair[0])
		unix.CloseOnExec(pair[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, errMemoryOwner
	}
	parent := os.NewFile(uintptr(pair[0]), "memory owner parent")
	child := os.NewFile(uintptr(pair[1]), "memory owner child")
	defer child.Close()
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		return nil, errMemoryOwner
	}
	shim := memoryOwnerShim
	if options.shim != "" {
		shim = options.shim + "\n" + shim
	}
	cmd := exec.Command(rt.Python, "-I", "-B", "-c", shim)
	cmd.Dir = store
	cmd.Env = []string{"HOME=" + store, "PATH=" + filepath.Dir(rt.Python) + ":/usr/bin:/bin", "LANG=C.UTF-8", "HF_HOME=" + filepath.Join(store, "models"), "XDG_CACHE_HOME=" + filepath.Join(store, "models"), "PYTHONDONTWRITEBYTECODE=1", "HF_HUB_DISABLE_IMPLICIT_TOKEN=1"}
	cmd.ExtraFiles = []*os.File{child, lock}
	// Arbitrary provider output has no diagnostic or persistence sink.
	output := &memoryOwnerOutput{stop: func() { _ = conn.(*net.UnixConn).CloseWrite() }}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = time.Second
	if cmd.Start() != nil {
		_ = conn.Close()
		return nil, errMemoryOwner
	}
	_ = child.Close()
	_ = lock.Close()
	owner := &memoryOwner{channel: conn, done: make(chan struct{})}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 64), 64)
		initialized := false
		released := false
		for scanner.Scan() {
			switch scanner.Text() {
			case "ready":
				if !initialized {
					initialized = true
					ready <- true
				} else {
					_ = conn.Close()
				}
			case "released":
				released = true
			case "failed":
				_ = conn.(*net.UnixConn).CloseWrite()
			default:
				_ = conn.(*net.UnixConn).CloseWrite()
			}
		}
		_ = conn.Close()
		waitErr := cmd.Wait()
		if !initialized {
			ready <- false
		}
		if !released || waitErr != nil {
			owner.err = errMemoryRelease
		}
		close(owner.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.(*net.UnixConn).CloseWrite()
		case <-owner.done:
		}
	}()
	// A context deadline bounds the small private initialization write as well.
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetWriteDeadline(deadline)
	if _, err = conn.Write(append(data, '\n')); err != nil {
		_ = owner.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errMemoryOwner
	}
	select {
	case <-ctx.Done():
		_ = owner.close()
		return nil, ctx.Err()
	case ok := <-ready:
		if !ok || output.bytes.Load() > 64<<10 {
			_ = owner.close()
			return nil, errMemoryOwner
		}
		if ctx.Err() != nil {
			_ = owner.close()
			return nil, ctx.Err()
		}
		return owner, nil
	}
}

func (o *memoryOwner) close() error {
	o.once.Do(func() { _ = o.channel.(*net.UnixConn).CloseWrite() })
	timer := time.NewTimer(memoryOwnerJoin)
	defer timer.Stop()
	select {
	case <-o.done:
		return o.err
	case <-timer.C:
		return errMemoryRelease
	}
}
