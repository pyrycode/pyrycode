package apps

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// VerifyBuild independently checks a retained build without creating or repairing it.
func VerifyBuild(ctx context.Context, appsRoot, serverID, appID, buildID string) (build RetainedBuild, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("apps: verify retained build: %w", err)
		}
	}()
	if !validID(serverID) || !validID(appID) || len(buildID) != 64 || strings.Trim(buildID, "0123456789abcdef") != "" {
		return RetainedBuild{}, ErrInvalidIdentity
	}
	root, e := privateDir(appsRoot)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer root.Close()
	parent, e := buildParent(root, appID, false)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer parent.Close()
	return verifyAt(ctx, parent, buildID, serverID, appID)
}
func verifyAt(ctx context.Context, parent *os.File, name, serverID, appID string) (RetainedBuild, error) {
	dir, e := artifactDirAt(parent, name, false)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer dir.Close()
	result, e := scanArtifacts(ctx, dir, nil, serverID, appID, true)
	if e == nil && len(name) == 64 && result.BuildID != name {
		e = ErrInvalidStorage
	}
	return result, e
}
func buildParent(root *os.File, appID string, create bool) (*os.File, error) {
	app, e := artifactDirAt(root, appID, create)
	if e != nil {
		return nil, e
	}
	defer app.Close()
	return artifactDirAt(app, "builds", create)
}

// InstallBuild copies and seals completed staging artifacts before exclusive publication.
// Existing destinations must verify; failures never repair them or change app data.
func InstallBuild(ctx context.Context, appsRoot, staging, serverID, appID string) (build RetainedBuild, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("apps: install retained build: %w", err)
		}
	}()
	if !validID(serverID) || !validID(appID) {
		return RetainedBuild{}, ErrInvalidIdentity
	}
	source, e := privateDir(staging)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer source.Close()
	root, e := privateDir(appsRoot)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer root.Close()
	app, e := artifactDirAt(root, appID, true)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer app.Close()
	parent, e := artifactDirAt(app, "builds", true)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer parent.Close()
	temp := ".install-" + rand.Text()
	if e = unix.Mkdirat(int(parent.Fd()), temp, 0700); e != nil {
		return RetainedBuild{}, fmt.Errorf("allocate temporary retained build: %w", e)
	}
	defer removeArtifactTemp(parent, temp)
	dir, e := artifactDirAt(parent, temp, false)
	if e != nil {
		return RetainedBuild{}, e
	}
	defer dir.Close()
	result, e := scanArtifacts(ctx, source, dir, serverID, appID, false)
	if e != nil {
		return RetainedBuild{}, e
	}
	inventory, e := regularAt(dir, "inventory.json", true)
	if e != nil {
		return RetainedBuild{}, e
	}
	_, e = inventory.Write(result.Inventory)
	if e == nil {
		e = inventory.Chmod(0400)
	}
	if e == nil {
		e = inventory.Sync()
	}
	ce := inventory.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return RetainedBuild{}, e
	}
	if e = dir.Chmod(0500); e != nil {
		return RetainedBuild{}, e
	}
	checked, e := verifyAt(ctx, parent, temp, serverID, appID)
	if e != nil {
		return RetainedBuild{}, e
	}
	if checked.BuildID != result.BuildID {
		return RetainedBuild{}, ErrInvalidStorage
	}
	if e = dir.Sync(); e != nil {
		return RetainedBuild{}, e
	}
	if e = ctx.Err(); e != nil {
		return RetainedBuild{}, e
	}
	e = renameArtifact(int(parent.Fd()), temp, int(parent.Fd()), result.BuildID)
	if e != nil && !errors.Is(e, unix.EEXIST) {
		return RetainedBuild{}, fmt.Errorf("publish retained build: %w", e)
	}
	if e = app.Sync(); e != nil {
		return RetainedBuild{}, e
	}
	if e = parent.Sync(); e != nil {
		return RetainedBuild{}, e
	}
	return verifyAt(ctx, parent, result.BuildID, serverID, appID)
}

// Only descriptor-relative operations clean the newly allocated temporary tree.
func removeArtifactTemp(parent *os.File, name string) {
	dir, e := artifactDirAt(parent, name, false)
	if e != nil {
		return
	} // Rename may have consumed it.
	defer dir.Close()
	_ = dir.Chmod(0700)
	entries, e := dir.ReadDir(-1)
	if e != nil {
		return
	} // Best-effort cleanup after a failed installation.
	for _, entry := range entries {
		var st unix.Stat_t
		if unix.Fstatat(int(dir.Fd()), entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
			continue
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			removeArtifactTemp(dir, entry.Name())
		} else {
			_ = unix.Unlinkat(int(dir.Fd()), entry.Name(), 0)
		}
	}
	_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}
