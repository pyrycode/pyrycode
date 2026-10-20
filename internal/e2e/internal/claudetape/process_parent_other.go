//go:build !darwin && !linux

package claudetape

import "os"

// Observer-parent ownership is deliberately unavailable without a kernel reader.
func currentProcessParent(pid int) (int, error) { return 0, os.ErrInvalid }
