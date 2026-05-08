package update

import (
	"errors"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		goos    string
		goarch  string
		want    string
		wantErr error
	}{
		{name: "darwin_arm64", version: "v0.9.1", goos: "darwin", goarch: "arm64", want: "pyry_0.9.1_Darwin_arm64.tar.gz"},
		{name: "darwin_amd64", version: "v0.9.1", goos: "darwin", goarch: "amd64", want: "pyry_0.9.1_Darwin_x86_64.tar.gz"},
		{name: "linux_arm64", version: "v0.9.1", goos: "linux", goarch: "arm64", want: "pyry_0.9.1_Linux_arm64.tar.gz"},
		{name: "linux_amd64", version: "v0.9.1", goos: "linux", goarch: "amd64", want: "pyry_0.9.1_Linux_x86_64.tar.gz"},
		{name: "version_no_v_prefix", version: "0.9.1", goos: "darwin", goarch: "arm64", want: "pyry_0.9.1_Darwin_arm64.tar.gz"},
		{name: "unsupported_os_windows", version: "v0.9.1", goos: "windows", goarch: "amd64", wantErr: ErrUnsupportedPlatform},
		{name: "unsupported_os_freebsd", version: "v0.9.1", goos: "freebsd", goarch: "amd64", wantErr: ErrUnsupportedPlatform},
		{name: "unsupported_arch_386", version: "v0.9.1", goos: "linux", goarch: "386", wantErr: ErrUnsupportedPlatform},
		{name: "unsupported_arch_riscv", version: "v0.9.1", goos: "linux", goarch: "riscv64", wantErr: ErrUnsupportedPlatform},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := AssetName(tc.version, tc.goos, tc.goarch)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("AssetName(%q, %q, %q) = %q, want %q", tc.version, tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

func TestAssetName_ErrorMessageNamesInputs(t *testing.T) {
	t.Parallel()
	_, err := AssetName("v0.9.1", "windows", "amd64")
	if err == nil {
		t.Fatalf("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "windows") || !strings.Contains(msg, "amd64") {
		t.Errorf("error message %q must contain both %q and %q", msg, "windows", "amd64")
	}
}

const sampleChecksums = `abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  pyry_0.9.1_Darwin_arm64.tar.gz
1111111111111111111111111111111111111111111111111111111111111111  pyry_0.9.1_Darwin_x86_64.tar.gz
2222222222222222222222222222222222222222222222222222222222222222  pyry_0.9.1_Linux_arm64.tar.gz
3333333333333333333333333333333333333333333333333333333333333333  pyry_0.9.1_Linux_x86_64.tar.gz
`

func TestParseChecksumsFile(t *testing.T) {
	t.Parallel()

	const upperHexLine = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789  pyry_0.9.1_Darwin_arm64.tar.gz\n"

	tests := []struct {
		name      string
		text      string
		assetName string
		want      string
		wantErr   error
	}{
		{
			name:      "asset_present_first",
			text:      sampleChecksums,
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			want:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:      "asset_present_last",
			text:      sampleChecksums,
			assetName: "pyry_0.9.1_Linux_x86_64.tar.gz",
			want:      "3333333333333333333333333333333333333333333333333333333333333333",
		},
		{
			name:      "asset_missing",
			text:      sampleChecksums,
			assetName: "pyry_0.9.1_Linux_riscv64.tar.gz",
			wantErr:   ErrAssetNotInChecksums,
		},
		{
			name:      "empty_input",
			text:      "",
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			wantErr:   ErrMalformedChecksums,
		},
		{
			name:      "whitespace_only",
			text:      "\n\n  \n",
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			wantErr:   ErrMalformedChecksums,
		},
		{
			name:      "no_parseable_lines",
			text:      "hello world\nnot a checksum\n",
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			wantErr:   ErrMalformedChecksums,
		},
		{
			name:      "crlf_line_endings",
			text:      strings.ReplaceAll(sampleChecksums, "\n", "\r\n"),
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			want:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:      "uppercase_hex_normalised",
			text:      upperHexLine,
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			want:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:      "trailing_blank_line",
			text:      sampleChecksums + "\n\n",
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			want:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:      "garbage_then_valid",
			text:      "junk line\n" + sampleChecksums,
			assetName: "pyry_0.9.1_Darwin_arm64.tar.gz",
			want:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseChecksumsFile(tc.text, tc.assetName)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("ParseChecksumsFile(_, %q) = %q, want %q", tc.assetName, got, tc.want)
			}
		})
	}
}

func TestVerifySHA256(t *testing.T) {
	t.Parallel()

	const (
		emptyHash      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		helloWorldHash = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
		zeroHash       = "0000000000000000000000000000000000000000000000000000000000000000"
	)

	tests := []struct {
		name        string
		data        []byte
		expectedHex string
		wantErr     error
	}{
		{name: "empty_data_correct_hash", data: []byte{}, expectedHex: emptyHash},
		{name: "nonempty_data_correct_hash", data: []byte("hello world"), expectedHex: helloWorldHash},
		{name: "mismatch", data: []byte("hello world"), expectedHex: zeroHash, wantErr: ErrChecksumMismatch},
		{name: "case_insensitive_match", data: []byte("hello world"), expectedHex: strings.ToUpper(helloWorldHash)},
		{name: "empty_expected_hex", data: []byte("hello world"), expectedHex: "", wantErr: ErrChecksumMismatch},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := VerifySHA256(tc.data, tc.expectedHex)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
		})
	}
}

func TestVerifySHA256_MismatchMessageIncludesDigests(t *testing.T) {
	t.Parallel()
	const (
		helloWorldHash = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
		zeroHash       = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	err := VerifySHA256([]byte("hello world"), zeroHash)
	if err == nil {
		t.Fatalf("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, helloWorldHash) {
		t.Errorf("error message %q must contain actual digest %q", msg, helloWorldHash)
	}
	if !strings.Contains(msg, zeroHash) {
		t.Errorf("error message %q must contain expected digest %q", msg, zeroHash)
	}
}
