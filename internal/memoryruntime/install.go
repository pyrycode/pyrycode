// Package memoryruntime installs an isolated, pinned memory CLI. Installation
// establishes neither a running watcher nor index or agent-search readiness.
package memoryruntime

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed linux-amd64.lock.json
var lockData []byte

var (
	ErrUnsupported = errors.New("unsupported memory runtime target or mode")
	ErrUnsafe      = errors.New("unsafe runtime destination")
	ErrIntegrity   = errors.New("artifact integrity failure")
	ErrProbe       = errors.New("runtime verification failure")
)

// Options identifies the service account. Home defaults to the current user's
// home; an explicit Home must be existing, absolute, canonical and owned by the
// caller, with no symlink components. Progress receives only finite stage
// names synchronously; callers must return promptly and must not reenter Install.
type Options struct {
	Home, Mode string
	Progress   func(string)
}

// Runtime contains permanent absolute launch locations and verified versions.
// Launch is the memsearch console script; Python is its managed interpreter.
type Runtime struct {
	Launch, Python string
	Versions       map[string]string
}

// Failure identifies a recoverable setup stage without exposing child output.
type Failure struct {
	Stage string
	Err   error
}

func (f *Failure) Error() string {
	return "memory runtime: " + f.Stage + " failed; setup can be retried"
}
func (f *Failure) Unwrap() error { return f.Err }

type artifact struct{ Name, Version, File, URL, SHA256 string }
type lock struct {
	Target       string     `json:"target"`
	MinimumGLIBC string     `json:"minimum_glibc"`
	Python       string     `json:"python"`
	Bootstrap    string     `json:"bootstrap_pip"`
	Runtime      artifact   `json:"runtime"`
	Wheels       []artifact `json:"wheels"`
}
type publication struct{ Generation, Lock, Seal string }
type engine struct {
	pins      lock
	client    *http.Client
	provision func(context.Context, string, lock) error
	probe     func(context.Context, string, lock) (map[string]string, error)
}

const managed = ".pyry/memory/runtime"

// Install supports OpenAI mode on Ubuntu 24.04 amd64 with glibc >=2.39.
// It requires no credentials, starts no service and never resolves dependencies.
func Install(ctx context.Context, o Options) (Runtime, error) {
	if o.Mode != "openai" || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return Runtime{}, &Failure{"target", ErrUnsupported}
	}
	var p lock
	if err := json.Unmarshal(lockData, &p); err != nil {
		return Runtime{}, &Failure{"pins", err}
	}
	if err := supportedHost(ctx, p); err != nil {
		return Runtime{}, &Failure{"target", err}
	}
	return (engine{pins: p, client: &http.Client{Timeout: 5 * time.Minute}, provision: provision, probe: probe}).install(ctx, o)
}
func (e engine) install(ctx context.Context, o Options) (result Runtime, err error) {
	stage := "destination"
	defer func() {
		if err != nil {
			err = &Failure{stage, err}
		}
	}()
	progress := func(s string) {
		stage = s
		if o.Progress != nil {
			o.Progress(s)
		}
	}
	if o.Mode != "openai" {
		return result, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if o.Home == "" {
		o.Home, err = os.UserHomeDir()
		if err != nil {
			return result, err
		}
	}
	root, err := openPrivateHome(o.Home)
	if err != nil {
		return result, err
	}
	defer root.Close()
	for _, dir := range []string{managed, managed + "/cache"} {
		if err := privateDir(root, dir); err != nil {
			return result, err
		}
	}
	progress("waiting")
	holder, err := acquire(ctx, root, managed+"/install.lock")
	if err != nil {
		return result, err
	}
	defer holder.Close()
	data, err := json.Marshal(e.pins)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(data)
	identity := hex.EncodeToString(sum[:])
	progress("verify")
	current := managed + "/current.json"
	f, readErr := regularFile(root, current, os.O_RDONLY)
	if readErr == nil {
		var pub publication
		decodeErr := json.NewDecoder(http.MaxBytesReader(nil, f, 4096)).Decode(&pub)
		f.Close()
		if decodeErr == nil && pub.Lock == identity && strings.HasPrefix(pub.Generation, "install-") && !strings.Contains(pub.Generation, "/") {
			gen := managed + "/" + pub.Generation
			seal, sealErr := treeSeal(ctx, root, gen, false)
			if sealErr != nil && errors.Is(sealErr, ErrUnsafe) {
				return result, sealErr
			}
			if sealErr == nil && seal == pub.Seal {
				versions, probeErr := e.probe(ctx, filepath.Join(root.Name(), gen), e.pins)
				if probeErr == nil {
					progress("ready")
					return launch(root, gen, e.pins, versions), nil
				}
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return result, readErr
	}
	progress("download")
	paths := make([]string, 0, len(e.pins.Wheels)+1)
	for _, a := range append([]artifact{e.pins.Runtime}, e.pins.Wheels...) {
		p, err := download(ctx, root, managed+"/cache", a, e.client)
		if err != nil {
			return result, err
		}
		paths = append(paths, p)
	}
	gen := managed + "/install-" + randomName()
	if err := root.Mkdir(gen, 0700); err != nil {
		return result, err
	}
	// Unpublished attempts have unique permanent paths. No retry writes into one.
	progress("extract")
	if err := extract(ctx, root, gen, paths[0]); err != nil {
		return result, err
	}
	if err := privateDir(root, gen+"/home"); err != nil {
		return result, err
	}
	var requirements strings.Builder
	for i, a := range e.pins.Wheels {
		if err := validateWheel(root, paths[i+1]); err != nil {
			return result, err
		}
		fmt.Fprintf(&requirements, "%s --hash=sha256:%s\n", filepath.Join(root.Name(), paths[i+1]), a.SHA256)
	}
	if err := root.WriteFile(gen+"/requirements.txt", []byte(requirements.String()), 0600); err != nil {
		return result, err
	}
	progress("install")
	if err := e.provision(ctx, filepath.Join(root.Name(), gen), e.pins); err != nil {
		return result, err
	}
	progress("verify")
	versions, err := e.probe(ctx, filepath.Join(root.Name(), gen), e.pins)
	if err != nil {
		return result, err
	}
	seal, err := treeSeal(ctx, root, gen, true)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	progress("publish")
	pub, err := json.Marshal(publication{filepath.Base(gen), identity, seal})
	if err != nil {
		return result, err
	}
	if err := publish(root, current, pub); err != nil {
		return result, err
	}
	progress("ready")
	return launch(root, gen, e.pins, versions), nil
}
func launch(root *os.Root, gen string, p lock, v map[string]string) Runtime {
	base := filepath.Join(root.Name(), gen, "python/bin")
	return Runtime{Launch: filepath.Join(base, "memsearch"), Python: filepath.Join(base, pythonName(p.Python)), Versions: v}
}

func pythonName(version string) string {
	parts := strings.Split(version, ".")
	return "python" + strings.Join(parts[:min(2, len(parts))], ".")
}
