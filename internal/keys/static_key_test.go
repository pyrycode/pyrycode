package keys

import (
	"bytes"
	"crypto/ecdh"
	"strings"
	"testing"
)

func TestValidDaemonName_AllowlistMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"single dot", ".", false},
		{"double dot", "..", false},
		{"forward slash", "foo/bar", false},
		{"traversal", "foo/../bar", false},
		{"backslash", "foo\\bar", false},
		{"uppercase", "Foo", false},
		{"embedded dot", "foo.bar", false},
		{"leading hyphen", "-leading", false},
		{"space", "foo bar", false},
		{"nul byte", "foo\x00bar", false},
		{"oversize", strings.Repeat("a", 65), false},
		{"default", "default", true},
		{"prod", "prod", true},
		{"hyphen non-leading", "dev-1", true},
		{"underscore", "my_daemon", true},
		{"digit prefix", "0abc", true},
		{"single char", "a", true},
		{"mixed", "a-1_b", true},
		{"exact limit", strings.Repeat("a", 64), true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := validDaemonName(tt.input)
			if got != tt.want {
				t.Errorf("validDaemonName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewStaticKey_PublicKeyMatchesPrivate(t *testing.T) {
	t.Parallel()
	sk := newStaticKey()
	priv, err := ecdh.X25519().NewPrivateKey(sk.priv[:])
	if err != nil {
		t.Fatalf("ecdh.NewPrivateKey: %v", err)
	}
	derived := priv.PublicKey().Bytes()
	if !bytes.Equal(derived, sk.pub[:]) {
		t.Errorf("derived public != stored public")
	}
}

func TestNewStaticKey_KeysAreNonZero(t *testing.T) {
	t.Parallel()
	a := newStaticKey()
	b := newStaticKey()

	var zero [32]byte
	if a.priv == zero {
		t.Error("private key is zero")
	}
	if a.pub == zero {
		t.Error("public key is zero")
	}
	if a.priv == b.priv {
		t.Error("two newStaticKey calls produced identical private keys")
	}
	if a.pub == b.pub {
		t.Error("two newStaticKey calls produced identical public keys")
	}
}

func TestStaticKey_AccessorsReturnByValue(t *testing.T) {
	t.Parallel()
	sk := newStaticKey()
	originalPriv := sk.priv
	originalPub := sk.pub

	priv := sk.PrivateKey()
	priv[0] ^= 0xff
	priv[31] ^= 0xff
	if sk.priv != originalPriv {
		t.Error("PrivateKey() return aliases internal storage")
	}

	pub := sk.PublicKey()
	pub[0] ^= 0xff
	pub[31] ^= 0xff
	if sk.pub != originalPub {
		t.Error("PublicKey() return aliases internal storage")
	}

	if sk.PrivateKey() != originalPriv {
		t.Error("PrivateKey() changed across calls")
	}
	if sk.PublicKey() != originalPub {
		t.Error("PublicKey() changed across calls")
	}
}
