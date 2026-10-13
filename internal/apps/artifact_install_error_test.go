package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testArtifactErrorContext(t *testing.T, err, cause error, operation string) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("error=%v, want wrapped %v", err, cause)
	}
	if !strings.Contains(err.Error(), operation) {
		t.Fatalf("error=%v, missing operation %q", err, operation)
	}
}

func TestRetainedErrorContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, level := range []string{"root", "app", "builds", "build"} {
		t.Run("verify missing "+level, func(t *testing.T) {
			root := testArtifactTemp(t)
			switch level {
			case "root":
				root = filepath.Join(root, "missing")
			case "builds":
				if err := os.Mkdir(filepath.Join(root, testApp), 0700); err != nil {
					t.Fatal(err)
				}
			case "build":
				if err := os.MkdirAll(filepath.Join(root, testApp, "builds"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := testArtifactSnapshot(t, root)
			_, err := VerifyBuild(ctx, root, testHost, testApp, strings.Repeat("a", 64))
			testArtifactErrorContext(t, err, os.ErrNotExist, "verify retained build")
			if testArtifactSnapshot(t, root) != before {
				t.Fatal("verification changed missing-build layout")
			}
		})
	}
	t.Run("validate missing staging", func(t *testing.T) {
		_, err := ValidateArtifacts(ctx, filepath.Join(testArtifactTemp(t), "missing"), testHost, testApp)
		testArtifactErrorContext(t, err, os.ErrNotExist, "validate retained artifacts")
	})
	t.Run("install missing staging", func(t *testing.T) {
		_, err := InstallBuild(ctx, testArtifactTemp(t), filepath.Join(testArtifactTemp(t), "missing"), testHost, testApp)
		testArtifactErrorContext(t, err, os.ErrNotExist, "install retained build")
	})
	for _, name := range []string{testApp, testApp + "/builds"} {
		t.Run("install nondirectory "+name, func(t *testing.T) {
			root := testArtifactTemp(t)
			testArtifactWrite(t, root, name, "existing")
			before := testArtifactSnapshot(t, root)
			_, err := InstallBuild(ctx, root, testArtifactTree(t), testHost, testApp)
			testArtifactErrorContext(t, err, unix.ENOTDIR, "install retained build")
			testArtifactErrorContext(t, err, unix.ENOTDIR, "open artifact directory")
			if testArtifactSnapshot(t, root) != before {
				t.Fatal("installation changed rejected parent")
			}
		})
	}
}

func TestArtifactDirectoryErrorContext(t *testing.T) {
	t.Parallel()
	parent, err := privateDir(testArtifactTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	for _, create := range []bool{false, true} {
		operation := "open artifact directory"
		if create {
			operation = "create artifact directory"
		}
		t.Run(operation, func(t *testing.T) {
			_, err := artifactDirAt(parent, "child", create)
			testArtifactErrorContext(t, err, unix.EBADF, operation)
		})
	}
}

func TestRetainedTemporaryErrorContext(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	root, stage := testArtifactTemp(t), testArtifactTree(t)
	parent := filepath.Join(root, testApp, "builds")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0700); err != nil {
			t.Error(err)
		}
	})
	before := testArtifactSnapshot(t, root)
	_, err := InstallBuild(context.Background(), root, stage, testHost, testApp)
	testArtifactErrorContext(t, err, unix.EACCES, "install retained build")
	testArtifactErrorContext(t, err, unix.EACCES, "allocate temporary retained build")
	if testArtifactSnapshot(t, root) != before {
		t.Fatal("failed allocation changed build storage")
	}
}
