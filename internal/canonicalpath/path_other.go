//go:build !darwin

package canonicalpath

func nativePath(string) (string, bool) {
	return "", false
}
