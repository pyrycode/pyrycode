package apps

import "golang.org/x/sys/unix"

func renameArtifact(fromParent int, from string, toParent int, to string) error {
	return unix.Renameat2(fromParent, from, toParent, to, unix.RENAME_NOREPLACE)
}
