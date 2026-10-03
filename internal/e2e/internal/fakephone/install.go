package fakephone

import "crypto/sha256"

// InstallKey returns the Noise static private key of the one app install that
// holds token. Since #2734 the daemon binds a pairing to the static key of its
// first accepted connection and refuses the same token from any other key, so
// a test that reconnects, or opens a second connection with one token, must
// present the same key each time — as a real phone does, keeping one keypair
// per paired daemon. Deriving it from the token gives every helper in every
// package that key without threading it through. Any 32 bytes are a valid
// X25519 scalar.
func InstallKey(token string) []byte {
	sum := sha256.Sum256([]byte("fakephone install: " + token))
	return sum[:]
}
