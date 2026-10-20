package claudetape

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPSendFileRejectsRealAncestorAndLeafSymlinks(t *testing.T) {
	const conv = "55555555-5555-4555-8555-555555555555"
	const id = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	for _, relative := range []string{"work", "work/" + MCPSendFileName, ".pyry", ".pyry/test", ".pyry/test/conversations", ".pyry/test/conversations/" + conv, ".pyry/test/conversations/" + conv + "/attachments", ".pyry/test/conversations/" + conv + "/attachments/" + id, ".pyry/test/conversations/" + conv + "/attachments/" + id + "/" + MCPSendFileName} {
		t.Run(relative, func(t *testing.T) {
			home := t.TempDir()
			seedMCPUnitEffect(t, home, conv, id)
			if ValidateMCPSendFileEffect(home, conv, id) != nil {
				t.Fatal("positive current copy before symlink mutation")
			}
			original := filepath.Join(home, relative)
			foreign := filepath.Join(t.TempDir(), "foreign")
			if os.Rename(original, foreign) != nil || os.Symlink(foreign, original) != nil {
				t.Fatal("real symlink setup")
			}
			if ValidateMCPSendFileEffect(home, conv, id) == nil {
				t.Fatal("foreign symlink tree accepted as current conversation copy")
			}
		})
	}
}
func TestMCPPinnedTreeRejectsActualAncestorAndFileRebinding(t *testing.T) {
	for _, kind := range []string{"home", "work", "source-file"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			home := filepath.Join(base, "home")
			if os.Mkdir(home, 0700) != nil {
				t.Fatal("home")
			}
			seedMCPUnitEffect(t, home, "55555555-5555-4555-8555-555555555555", "ffffffff-ffff-4fff-8fff-ffffffffffff")
			tree, e := openMCPFileTree(home)
			if e != nil {
				t.Fatal(e)
			}
			defer tree.Close()
			dir, e := tree.dir("work")
			if e != nil {
				t.Fatal(e)
			}
			if raw, e := tree.read(dir, MCPSendFileName, len(MCPSendFileBody)); e != nil || string(raw) != MCPSendFileBody {
				t.Fatal("real pinned source read")
			}
			path := home
			switch kind {
			case "work":
				path = filepath.Join(home, "work")
			case "source-file":
				path = filepath.Join(home, "work", MCPSendFileName)
			}
			old := filepath.Join(base, "old")
			if os.Rename(path, old) != nil {
				t.Fatal("actual rebind")
			}
			if kind == "source-file" {
				if os.WriteFile(path, []byte(MCPSendFileBody), 0600) != nil {
					t.Fatal("replacement")
				}
			} else {
				if os.Mkdir(path, 0700) != nil {
					t.Fatal("replacement")
				}
			}
			if tree.check() == nil {
				t.Fatal("detached old inode accepted after linked current source rebound")
			}
		})
	}
}
func TestMCPBoundedSourceReadRejectsFIFOAndOversize(t *testing.T) {
	home := t.TempDir()
	if os.Chmod(home, 0700) != nil {
		t.Fatal("private unit HOME")
	}
	if os.Mkdir(filepath.Join(home, "work"), 0700) != nil {
		t.Fatal("work")
	}
	tree, e := openMCPFileTree(home)
	if e != nil {
		t.Fatal(e)
	}
	defer tree.Close()
	dir, e := tree.dir("work")
	if e != nil {
		t.Fatal(e)
	}
	if os.WriteFile(filepath.Join(home, "work", MCPSendFileName), []byte(MCPSendFileBody+"extra"), 0600) != nil {
		t.Fatal("oversize")
	}
	if unix.Mkfifo(filepath.Join(home, "work", "controlled.fifo"), 0600) != nil {
		t.Fatal("actual FIFO")
	}
	if _, e := tree.read(dir, "controlled.fifo", len(MCPSendFileBody)); e == nil {
		t.Fatal("FIFO accepted as regular bounded source")
	}
	if _, e := tree.read(dir, MCPSendFileName, len(MCPSendFileBody)); e == nil {
		t.Fatal("oversize actual source read")
	}
}
