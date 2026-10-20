package claudetape

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// PrepareSessionSnapshotDirectory creates only the canonical native parent,
// relative to a no-follow HOME descriptor. No ancestor symlink is traversed.
func PrepareSessionSnapshotDirectory(home, session string) error {
	fail := errors.New("cannot prepare owned native transcript directory")
	if !poolUUID.MatchString(session) || !filepath.IsAbs(home) {
		return fail
	}
	relative, err := filepath.Rel(home, filepath.Dir(LifecycleNativePath(home, session)))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fail
	}
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return fail
		}
		if err := unix.Mkdirat(fd, part, 0700); err != nil && err != unix.EEXIST {
			unix.Close(fd)
			return fail
		}
		child, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return fail
		}
		fd = child
	}
	return unix.Close(fd)
}
