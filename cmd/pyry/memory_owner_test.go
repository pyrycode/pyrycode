package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/memoryruntime"
)

func testMemoryOwner(t *testing.T, model string) (memoryruntime.Runtime, config.MemorySettings, effectiveMemory, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(home, "vault")
	if err := os.Mkdir(vault, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return memoryruntime.Runtime{Python: "/usr/bin/python3", Versions: map[string]string{"memsearch": "0.4.21", "milvus-lite": "2.5.1", "pymilvus": "2.5.16"}}, config.MemorySettings{Embedding: config.MemoryEmbedding{Provider: "local", Model: model}}, effectiveMemory{VaultPath: vault, SearchRoots: []string{vault}}, home
}

func TestMemoryOwnerCancellation(t *testing.T) {
	rt, s, e, _ := testMemoryOwner(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := startMemoryOwnerWith(ctx, rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("unbounded cancellation")
	}
}

// Controlled provider/backend replacements execute inside the production worker.
// Only dependency behavior changes; controller, fd inheritance and channel are real.
const testMemoryShim = `
import sys, types, subprocess, signal, os, time, json
hf=types.ModuleType('huggingface_hub')
def snapshot_download(model,local_dir):
    os.makedirs(local_dir,mode=0o700,exist_ok=True)
    return local_dir
hf.snapshot_download=snapshot_download
sys.modules['huggingface_hub']=hf
st=types.ModuleType('sentence_transformers')
st.SentenceTransformer=lambda *a,**k:None
sys.modules['sentence_transformers']=st
class Embedder:
    dimension=3
    async def embed(self,texts):
        assert texts==['pyry managed memory health probe']
        if getattr(self,'fail',False):raise ValueError('synthetic-'+'secret '+ 'x'*200000)
        return [[1.,0.,0.]]
class Store:
    def __init__(self,path,model):
        self.path=path;self.model=model;self.backend=None
    def _ensure_collection_for_write(self):
        self.backend=subprocess.Popen(['/usr/bin/python3','-c',
            "import os,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(120)"])
        with open(os.path.join(os.path.dirname(self.path),'backend.pid'),'w') as f:f.write(str(self.backend.pid))
        if not os.path.exists(self.path):
            with open(self.path,'w') as f:f.write('existing contents')
        if self.model=='workerexit':os._exit(7)
    def _connected_client(self):
        assert self.backend is not None
        return self
    def list_collections(self):return []
class MemSearch:
    def __init__(self,paths,embedding_provider,embedding_model,embedding_api_key,embedding_base_url,milvus_uri):
        assert paths==[]
        assert 'OPENAI_API_KEY' not in os.environ and 'OPENAI_BASE_URL' not in os.environ
        assert 'PLANTED_SECRET' not in os.environ
        secret=('synthetic-'+'secret').encode()
        if sys.platform=='linux':
            with open('/proc/self/cmdline','rb') as f:assert secret not in f.read()
        else:
            assert secret not in subprocess.check_output(['/bin/ps','-p',str(os.getpid()),'-o','command='])
        if embedding_provider=='openai':assert embedding_api_key==('synthetic-'+'secret')
        self.model=embedding_model
        if self.model=='hang':time.sleep(120)
        if self.model=='fail':
            print(('synthetic-'+'secret')+'x'*200000)
            print(('synthetic-'+'secret')+'x'*200000,file=sys.stderr)
        if self.model=='fail' and embedding_provider=='local':raise ValueError('synthetic-'+'secret '+ 'x'*200000)
        self._embedder=Embedder()
        self._embedder.fail=self.model=='fail'
        if self.model=='dimension':self._embedder.dimension=4
        self.store=Store(milvus_uri,self.model)
        with open(os.path.join(os.path.dirname(milvus_uri),'worker.pid'),'w') as f:f.write(str(os.getpid()))
    def close(self):
        if self.model=='force':
            signal.signal(signal.SIGTERM,signal.SIG_IGN)
            time.sleep(120)
        if self.store.backend is not None:
            self.store.backend.kill();self.store.backend.wait()
module=types.ModuleType('memsearch');module.MemSearch=MemSearch
sys.modules['memsearch']=module
`

func testStartMemoryOwner(t *testing.T, rt memoryruntime.Runtime, s config.MemorySettings, e effectiveMemory) *memoryOwner {
	t.Helper()
	owner, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim, credential: func(context.Context, string) (string, error) { return "synthetic-secret", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.close(); err != nil {
			t.Error(err)
		}
	})
	return owner
}
func TestMemoryOwnerProviders(t *testing.T) {
	for _, provider := range []string{"local", "openai"} {
		t.Run(provider, func(t *testing.T) {
			rt, s, e, home := testMemoryOwner(t, "model")
			s.Embedding.Provider = provider
			t.Setenv("OPENAI_API_KEY", "synthetic-secret")
			t.Setenv("OPENAI_BASE_URL", "http://wrong")
			t.Setenv("PLANTED_SECRET", "synthetic-secret")
			owner := testStartMemoryOwner(t, rt, s, e)
			if err := owner.close(); err != nil {
				t.Fatal(err)
			}
			err := filepath.WalkDir(filepath.Join(home, ".pyry", "memory", "stores"), func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if strings.Contains(string(data), "synthetic-secret") {
						t.Fatal("persisted secret")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			owner = testStartMemoryOwner(t, rt, s, e)
			if err := owner.close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMemoryOwnerFailures(t *testing.T) {
	for _, provider := range []string{"local", "openai"} {
		t.Run(provider, func(t *testing.T) {
			rt, s, e, _ := testMemoryOwner(t, "fail")
			s.Embedding.Provider = provider
			owner, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim, credential: func(context.Context, string) (string, error) { return "synthetic-secret", nil }})
			if owner != nil || !errors.Is(err, errMemoryOwner) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("failure: %v", err)
			}
			s.Embedding.Model = "model"
			testStartMemoryOwner(t, rt, s, e)
		})
	}
}
func TestMemoryOwnerExclusion(t *testing.T) {
	rt, s, e, _ := testMemoryOwner(t, "model")
	owner := testStartMemoryOwner(t, rt, s, e)
	_, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
	if !errors.Is(err, errMemoryOccupied) {
		t.Fatalf("competing owner: %v", err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	testStartMemoryOwner(t, rt, s, e)
}
func TestMemoryOwnerCompatibility(t *testing.T) {
	rt, s, e, home := testMemoryOwner(t, "model")
	owner := testStartMemoryOwner(t, rt, s, e)
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	stores, _ := filepath.Glob(filepath.Join(home, ".pyry", "memory", "stores", "*"))
	database := filepath.Join(stores[0], "milvus.db")
	before, _ := os.ReadFile(database)
	for _, change := range []string{"model", "provider", "format", "dimension"} {
		t.Run(change, func(t *testing.T) {
			changed := s
			runtime := rt
			runtime.Versions = maps.Clone(rt.Versions)
			switch change {
			case "model":
				changed.Embedding.Model = "other"
			case "provider":
				changed.Embedding.Provider = "openai"
			case "format":
				runtime.Versions["milvus-lite"] = "3.0"
			case "dimension":
				p := filepath.Join(stores[0], "compatibility.json")
				data, _ := os.ReadFile(p)
				data = bytes.Replace(data, []byte(`"dimension": 3`), []byte(`"dimension": 4`), 1)
				if err := os.WriteFile(p, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := startMemoryOwnerWith(context.Background(), runtime, changed, e, memoryOwnerOptions{shim: testMemoryShim, credential: func(context.Context, string) (string, error) { return "synthetic-secret", nil }})
			if err == nil {
				t.Fatal("incompatible admitted")
			}
			after, _ := os.ReadFile(database)
			if !bytes.Equal(before, after) {
				t.Fatal("contents changed")
			}
		})
	}
}
func TestMemoryOwnerStalePID(t *testing.T) {
	rt, s, e, home := testMemoryOwner(t, "model")
	owner := testStartMemoryOwner(t, rt, s, e)
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	stores, _ := filepath.Glob(filepath.Join(home, ".pyry", "memory", "stores", "*"))
	if err := os.WriteFile(filepath.Join(stores[0], "owner.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	testStartMemoryOwner(t, rt, s, e)
}
func TestMemoryOwnerShutdown(t *testing.T) {
	rt, s, e, _ := testMemoryOwner(t, "force")
	owner := testStartMemoryOwner(t, rt, s, e)
	started := time.Now()
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > memoryOwnerJoin {
		t.Fatal("unbounded stop")
	}
	s.Embedding.Model = "force"
	testStartMemoryOwner(t, rt, s, e)
}
func TestMemoryOwnerUnsafeDestinations(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "permissions", "source"} {
		t.Run(kind, func(t *testing.T) {
			rt, s, e, home := testMemoryOwner(t, "model")
			store, lock, err := memoryOwnerStorage(context.Background(), home, e)
			if err != nil {
				t.Fatal(err)
			}
			_ = lock.Close()
			target := filepath.Join(home, "target")
			if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				err = os.Symlink(target, filepath.Join(store, "bad"))
			case "hardlink":
				err = os.Link(target, filepath.Join(store, "bad"))
			case "permissions":
				err = os.Chmod(store, 0755)
			case "source":
				e.SearchRoots = append(e.SearchRoots, home)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
			if !errors.Is(err, errMemoryDestination) {
				t.Fatalf("unsafe: %v", err)
			}
			data, _ := os.ReadFile(target)
			if string(data) != "untouched" {
				t.Fatal("target modified")
			}
		})
	}
}

func TestMemoryOwnerProcessDeath(t *testing.T) {
	for _, event := range []string{"worker", "parent"} {
		t.Run(event, func(t *testing.T) {
			rt, s, e, home := testMemoryOwner(t, "model")
			if event == "worker" {
				owner := testStartMemoryOwner(t, rt, s, e)
				stores, _ := filepath.Glob(filepath.Join(home, ".pyry", "memory", "stores", "*"))
				data, err := os.ReadFile(filepath.Join(stores[0], "worker.pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(data))
				if err != nil {
					t.Fatal(err)
				}
				// This controlled direct worker remains owned by its production controller.
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				_, err = startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
				if !errors.Is(err, errMemoryOccupied) {
					t.Fatalf("surviving backend exclusion: %v", err)
				}
				if err := owner.close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryOwnerHelperProcess$")
				cmd.Env = append(os.Environ(), "PYRY_OWNER_HELPER="+e.VaultPath)
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				line, err := bufio.NewReader(stdout).ReadString('\n')
				if err != nil || line != "ready\n" {
					t.Fatalf("helper: %q %v", line, err)
				}
				_, err = startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
				if !errors.Is(err, errMemoryOccupied) {
					t.Fatalf("cross-process contention: %v", err)
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
			}
			deadline := time.Now().Add(memoryOwnerJoin)
			for {
				owner, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
				if err == nil {
					if err := owner.close(); err != nil {
						t.Fatal(err)
					}
					break
				}
				if !errors.Is(err, errMemoryOccupied) || time.Now().After(deadline) {
					t.Fatalf("release retry: %v", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
func TestMemoryOwnerHelperProcess(t *testing.T) {
	vault := os.Getenv("PYRY_OWNER_HELPER")
	if vault == "" {
		return
	}
	rt := memoryruntime.Runtime{Python: "/usr/bin/python3", Versions: map[string]string{"memsearch": "0.4.21", "milvus-lite": "2.5.1", "pymilvus": "2.5.16"}}
	s := config.MemorySettings{Embedding: config.MemoryEmbedding{Provider: "local", Model: "model"}}
	owner, err := startMemoryOwnerWith(context.Background(), rt, s, effectiveMemory{VaultPath: vault, SearchRoots: []string{vault}}, memoryOwnerOptions{shim: testMemoryShim})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	fmt.Println("ready")
	select {}
}

func TestMemoryOwnerSurvivingBackend(t *testing.T) {
	rt, s, e, home := testMemoryOwner(t, "model")
	// Escape the controlled backend from the group, preserving inherited fd 4.
	shim := strings.Replace(testMemoryShim, `time.sleep(120)"])`, `time.sleep(120)"],start_new_session=True)`, 1)
	owner, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: shim})
	if err != nil {
		t.Fatal(err)
	}
	stores, _ := filepath.Glob(filepath.Join(home, ".pyry", "memory", "stores", "*"))
	readPID := func(name string) int {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(stores[0], name))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Fatal(err)
		}
		return pid
	}
	worker := readPID("worker.pid")
	backend := readPID("backend.pid")
	t.Cleanup(func() { _ = syscall.Kill(backend, syscall.SIGKILL); _ = owner.close() })
	if err := syscall.Kill(worker, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := owner.close(); !errors.Is(err, errMemoryRelease) {
		t.Fatalf("incomplete release: %v", err)
	}
	if time.Since(started) > memoryOwnerJoin {
		t.Fatal("unbounded cleanup")
	}
	if _, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim}); !errors.Is(err, errMemoryOccupied) {
		t.Fatalf("lost surviving-backend exclusion: %v", err)
	}
	// Fixture PID identity was observed directly while the backend retained its fd;
	// production never uses this bookkeeping to signal an escaped process.
	if err := syscall.Kill(backend, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		reopened, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
		if err == nil {
			if err := reopened.close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(err, errMemoryOccupied) || time.Now().After(deadline) {
			t.Fatalf("after release: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMemoryOwnerFreshCredential(t *testing.T) {
	rt, s, e, home := testMemoryOwner(t, "model")
	store := memoryCredentialStore{home: home, goos: "unsupported"}
	s.Embedding.Provider = "openai"
	s.Embedding.CredentialReference = testMemorySet(t, store, "synthetic-secret")
	owner, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	// The same saved reference now resolves a different token; the controlled
	// provider rejects it. A cached credential would incorrectly pass the probe.
	if ref := testMemorySet(t, store, memoryNew); ref != s.Embedding.CredentialReference {
		t.Fatal("reference changed")
	}
	if _, err := startMemoryOwnerWith(context.Background(), rt, s, e, memoryOwnerOptions{shim: testMemoryShim}); !errors.Is(err, errMemoryOwner) {
		t.Fatalf("fresh resolution: %v", err)
	}
}
