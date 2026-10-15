package apps

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const backupNodeBootstrap = "--pyry-internal-backup-node"

// The disposable child enters the inherited directory before replacing itself
// with Node. fchdir preserves directory identity across pathname substitutions.
func init() {
	if len(os.Args) != 4 || os.Args[1] != backupNodeBootstrap {
		return
	}
	err := execBackupNode(os.Args[2], os.Args[3])
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func execBackupNode(node, mode string) error {
	if !filepath.IsAbs(node) || mode != "snapshot" && mode != "verify" {
		return fmt.Errorf("apps: invalid snapshot bootstrap arguments")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(3, &stat); err != nil {
		return fmt.Errorf("apps: inspect snapshot directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("apps: snapshot descriptor is not a directory")
	}
	if err := unix.Fchdir(3); err != nil {
		return fmt.Errorf("apps: enter snapshot directory: %w", err)
	}
	if err := unix.Close(3); err != nil {
		return fmt.Errorf("apps: close snapshot directory: %w", err)
	}
	return fmt.Errorf("apps: exec snapshot node: %w", unix.Exec(node, []string{node, "-e", backupSQL, mode}, []string{}))
}
