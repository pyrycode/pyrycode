package memorycapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFilesystemAttacks(t *testing.T) {
	for _, attack := range []string{"symlink", "hardlink", "directory", "fifo", "permissions", "owner", "lock", "directory-symlink", "directory-mode", "directory-swap", "record-swap", "temp-swap", "lock-swap", "regular-swap"} {
		t.Run(attack, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			ctx := context.Background()
			b, err := s.Publish(ctx, testID, testPayload())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".pyry", "memory", "capture")
			file := filepath.Join(dir, recordName(testID))
			outside := filepath.Join(home, "outside")
			if err = os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			late := attack == "directory-swap" || attack == "record-swap" || attack == "temp-swap" || attack == "lock-swap" || attack == "regular-swap"
			mutate := func() {
				switch attack {
				case "symlink", "record-swap":
					if err = os.Remove(file); err == nil {
						err = os.Symlink(outside, file)
					}
				case "hardlink":
					if err = os.Remove(file); err == nil {
						err = os.Link(outside, file)
					}
				case "directory":
					if err = os.Remove(file); err == nil {
						err = os.Mkdir(file, 0700)
					}
				case "fifo":
					if err = os.Remove(file); err == nil {
						err = unix.Mkfifo(file, 0600)
					}
				case "permissions":
					err = os.Chmod(file, 0644)
				case "owner":
					if os.Geteuid() != 0 {
						t.Skip("ownership substitution requires root")
					}
					err = os.Chown(file, 12345, -1)
				case "regular-swap":
					raw, e := os.ReadFile(file)
					if e != nil {
						t.Fatal(e)
					}
					if err = os.Remove(file); err == nil {
						err = os.WriteFile(file, raw, 0600)
					}
				case "lock", "lock-swap":
					lock := filepath.Join(dir, ".lock")
					if err = os.Remove(lock); err == nil {
						err = os.Symlink(outside, lock)
					}
				case "directory-symlink":
					if err = os.Rename(dir, dir+"-old"); err == nil {
						err = os.Symlink(dir+"-old", dir)
					}
				case "directory-mode":
					err = os.Chmod(dir, 0755)
				case "directory-swap":
					if err = os.Rename(dir, dir+"-old"); err == nil {
						err = os.Mkdir(dir, 0700)
					}
				case "temp-swap":
					entries, e := os.ReadDir(dir)
					if e != nil {
						err = e
						break
					}
					for _, entry := range entries {
						if len(entry.Name()) > 5 && entry.Name()[:5] == ".tmp-" {
							p := filepath.Join(dir, entry.Name())
							if err = os.Remove(p); err == nil {
								err = os.Symlink(outside, p)
							}
							break
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if late {
				s.hook = func(stage string) error {
					if stage == "rename" {
						mutate()
					}
					return nil
				}
				if err = s.Report(ctx, testID, b.ID, []string{"new.md"}); err == nil {
					t.Fatal("accepted late substitution")
				}
			} else {
				mutate()
				if got, e := s.Pending(ctx, testID); e == nil || got != nil {
					t.Fatalf("unsafe read: %#v %v", got, e)
				}
				if got, e := s.Publish(ctx, testID, testPayload()); e == nil || got != nil {
					t.Fatalf("unsafe publish: %#v %v", got, e)
				}
			}
			raw, e := os.ReadFile(outside)
			if e != nil || string(raw) != "untouched" {
				t.Fatalf("outside changed: %q %v", raw, e)
			}
		})
	}
}

func TestCancelledLockAndPublication(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	lock, err := s.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := s.Publish(ctx, testID, testPayload()); done <- e }()
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	s.hook = func(stage string) error {
		if stage == "rename" {
			cancel()
		}
		return nil
	}
	if b, e := s.Publish(ctx, testID, testPayload()); b != nil || !errors.Is(e, context.Canceled) {
		t.Fatalf("cancelled publication: %#v %v", b, e)
	}
	if b := testPending(t, testStore(t, home), testID); b != nil {
		t.Fatal("cancelled batch published")
	}
}

func TestInvalidInputs(t *testing.T) {
	s := testStore(t, t.TempDir())
	ctx := context.Background()
	for _, id := range []Identity{{}, {Vault: "relative", Conversation: "c", Generation: "g"}, {Vault: "/vault/../other", Conversation: "c", Generation: "g"}, {Vault: "/vault", Conversation: "c", Generation: string([]byte{255})}} {
		if b, e := s.Publish(ctx, id, testPayload()); b != nil || !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid identity: %#v %v", b, e)
		}
	}
	for _, kind := range []string{"agent", "model", "revision", "utf8", "metadata"} {
		p := testPayload()
		switch kind {
		case "agent":
			p.Agent = ""
		case "model":
			p.Model = ""
		case "revision":
			p.Messages[0].Revision = ""
		case "utf8":
			p.Messages[0].Text = string([]byte{255})
		case "metadata":
			p.Metadata = []byte("{")
		}
		if b, e := s.Publish(ctx, testID, p); b != nil || !errors.Is(e, ErrInvalid) {
			t.Fatalf("%s: %#v %v", kind, b, e)
		}
	}
	if b := testPending(t, s, testID); b != nil {
		t.Fatal("invalid input published")
	}
}

func TestLateFirstPublication(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	file := filepath.Join(home, ".pyry/memory/capture", recordName(testID))
	s.hook = func(stage string) error {
		if stage == "rename" {
			return os.WriteFile(file, []byte("corrupt substitute"), 0600)
		}
		return nil
	}
	if b, err := s.Publish(context.Background(), testID, testPayload()); err == nil || b != nil {
		t.Fatalf("substitute executable: %#v %v", b, err)
	}
	raw, err := os.ReadFile(file)
	if err != nil || string(raw) != "corrupt substitute" {
		t.Fatalf("substitute overwritten: %q %v", raw, err)
	}
}

func TestPostRenameSubstitution(t *testing.T) {
	for _, leaf := range []string{"record", "lock", "directory"} {
		t.Run(leaf, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			dir := filepath.Join(home, ".pyry/memory/capture")
			s.hook = func(stage string) error {
				if stage != "dir-sync" {
					return nil
				}
				if leaf == "directory" {
					if e := os.Rename(dir, dir+"-old"); e != nil {
						return e
					}
					return os.Mkdir(dir, 0700)
				}
				name := recordName(testID)
				if leaf == "lock" {
					name = ".lock"
				}
				file := filepath.Join(dir, name)
				if e := os.Remove(file); e != nil {
					return e
				}
				return os.Symlink("missing", file)
			}
			if b, e := s.Publish(context.Background(), testID, testPayload()); b != nil || e == nil {
				t.Fatalf("unsafe renamed state executable: %#v %v", b, e)
			}
		})
	}
}

func TestPartialWriteFailure(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			ctx := context.Background()
			var before *Batch
			if existing {
				var e error
				before, e = s.Publish(ctx, testID, testPayload())
				if e != nil {
					t.Fatal(e)
				}
			}
			s.hook = func(stage string) error {
				if stage != "write" {
					return nil
				}
				entries, e := os.ReadDir(filepath.Join(home, ".pyry/memory/capture"))
				if e != nil {
					return e
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".tmp-") {
						if e = os.WriteFile(filepath.Join(home, ".pyry/memory/capture", entry.Name()), []byte(`{"partial`), 0600); e != nil {
							return e
						}
					}
				}
				return io.ErrShortWrite
			}
			if existing {
				if e := s.Report(ctx, testID, before.ID, []string{"a.md", "b.md"}); !errors.Is(e, io.ErrShortWrite) {
					t.Fatal(e)
				}
			} else {
				if b, e := s.Publish(ctx, testID, testPayload()); b != nil || !errors.Is(e, io.ErrShortWrite) {
					t.Fatalf("partial executable: %#v %v", b, e)
				}
			}
			if got := testPending(t, testStore(t, home), testID); !reflect.DeepEqual(got, before) {
				t.Fatalf("partial publication changed state: %#v", got)
			}
		})
	}
}
func TestPrivateModes(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	if _, err := s.Publish(context.Background(), testID, testPayload()); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".pyry", ".pyry/memory", ".pyry/memory/capture", ".pyry/memory/capture/.lock", filepath.Join(".pyry/memory/capture", recordName(testID))} {
		info, err := os.Stat(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o", rel, info.Mode().Perm())
		}
	}
}

func TestOwnershipValidation(t *testing.T) {
	// A real foreign-owned fixture requires chown privileges; the fstat boundary
	// is independently exercised here on every account.
	uid := uint32(os.Geteuid())
	for _, owner := range []uint32{uid, uid + 1} {
		t.Run(fmt.Sprint(owner), func(t *testing.T) {
			var st unix.Stat_t
			st.Uid, st.Nlink = owner, 1
			st.Mode = unix.S_IFREG | 0600
			if safeRecord(st) != (owner == uid) {
				t.Fatal("record ownership accepted incorrectly")
			}
			st.Mode = unix.S_IFDIR | 0700
			if safeDirectory(st, true, false) != (owner == uid) {
				t.Fatal("private directory ownership accepted incorrectly")
			}
			if safeDirectory(st, false, true) != (owner == uid) {
				t.Fatal("home ownership accepted incorrectly")
			}
		})
	}
}
