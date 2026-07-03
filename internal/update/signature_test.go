package update

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"
)

// fixedSeed yields a deterministic keypair so table cases are reproducible.
// A distinct seed drives the "wrong key" case.
var (
	fixedSeed = bytes.Repeat([]byte{0x01}, ed25519.SeedSize)
	otherSeed = bytes.Repeat([]byte{0x02}, ed25519.SeedSize)
)

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	priv := ed25519.NewKeyFromSeed(fixedSeed)
	pub := priv.Public().(ed25519.PublicKey)
	otherPub := ed25519.NewKeyFromSeed(otherSeed).Public().(ed25519.PublicKey)

	data := []byte("the exact bytes of checksums.txt")
	goodSig := ed25519.Sign(priv, data)

	tampered := bytes.Clone(data)
	tampered[0] ^= 0x01

	// A signature one byte too long and one byte too short — neither is
	// ed25519.SignatureSize, so both must be rejected without panicking.
	longSig := append(bytes.Clone(goodSig), 0x00)
	shortSig := goodSig[:ed25519.SignatureSize-1]

	emptySig := ed25519.Sign(priv, nil)

	tests := []struct {
		name    string
		data    []byte
		sig     []byte
		pub     ed25519.PublicKey
		wantErr error
	}{
		{
			name:    "valid signature and correct key",
			data:    data,
			sig:     goodSig,
			pub:     pub,
			wantErr: nil,
		},
		{
			name:    "tampered data",
			data:    tampered,
			sig:     goodSig,
			pub:     pub,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "wrong public key",
			data:    data,
			sig:     goodSig,
			pub:     otherPub,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "signature too long",
			data:    data,
			sig:     longSig,
			pub:     pub,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "signature too short",
			data:    data,
			sig:     shortSig,
			pub:     pub,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "nil signature",
			data:    data,
			sig:     nil,
			pub:     pub,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "wrong-length public key",
			data:    data,
			sig:     goodSig,
			pub:     pub[:ed25519.PublicKeySize-1],
			wantErr: ErrInvalidPublicKey,
		},
		{
			name:    "nil public key",
			data:    data,
			sig:     goodSig,
			pub:     nil,
			wantErr: ErrInvalidPublicKey,
		},
		{
			name:    "empty data with matching signature",
			data:    nil,
			sig:     emptySig,
			pub:     pub,
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := VerifySignature(tt.data, tt.sig, tt.pub)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("VerifySignature() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("VerifySignature() = %v, want errors.Is(_, %v)", err, tt.wantErr)
			}
		})
	}
}

// TestVerifySignature_MessageOmitsKeyMaterial pins the house-style contract
// that the error is a clean sentinel-carrier: neither the signature nor the
// key bytes leak into the message.
func TestVerifySignature_MessageOmitsKeyMaterial(t *testing.T) {
	t.Parallel()

	priv := ed25519.NewKeyFromSeed(fixedSeed)
	pub := priv.Public().(ed25519.PublicKey)
	data := []byte("checksums")
	badSig := ed25519.Sign(ed25519.NewKeyFromSeed(otherSeed), data)

	err := VerifySignature(data, badSig, pub)
	if err == nil {
		t.Fatal("VerifySignature() = nil, want error")
	}
	msg := err.Error()
	if bytes.Contains([]byte(msg), badSig) || bytes.Contains([]byte(msg), pub) {
		t.Errorf("error message leaks key/signature bytes: %q", msg)
	}
}
