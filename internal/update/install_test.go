package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"testing"
)

func buildTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, data := range files {
		hdr := &tar.Header{
			Name:     name,
			Size:     int64(len(data)),
			Mode:     0o755,
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%q): %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("Write(%q): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar.Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip.Close: %v", err)
	}
	return buf.Bytes()
}

func gzipOf(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatalf("gzip.Write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip.Close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	t.Parallel()

	pyryBytes := []byte("\x7fELF...fake binary contents...")
	fixture := buildTarGz(t, map[string][]byte{
		"pyry":            pyryBytes,
		"LICENSE":         []byte("MIT"),
		"docs/INSTALL.md": []byte("# Install"),
	})

	tests := []struct {
		name       string
		data       []byte
		binaryName string
		want       []byte
		wantErr    error
	}{
		{
			name:       "binary_present_returned_unchanged",
			data:       fixture,
			binaryName: "pyry",
			want:       pyryBytes,
		},
		{
			name:       "binary_missing",
			data:       fixture,
			binaryName: "claude",
			wantErr:    ErrBinaryNotInArchive,
		},
		{
			name:       "garbage_not_gzip",
			data:       []byte("this is plainly not a gzip stream"),
			binaryName: "pyry",
			wantErr:    ErrMalformedArchive,
		},
		{
			name:       "valid_gzip_garbage_tar",
			data:       gzipOf(t, []byte("not a tar archive at all")),
			binaryName: "pyry",
			wantErr:    ErrMalformedArchive,
		},
		{
			name:       "empty_input",
			data:       []byte{},
			binaryName: "pyry",
			wantErr:    ErrMalformedArchive,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ExtractBinary(tc.data, tc.binaryName)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("ExtractBinary returned %d bytes, want %d", len(got), len(tc.want))
			}
		})
	}
}
