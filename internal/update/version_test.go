package update

import (
	"errors"
	"testing"
)

func TestParseLatestRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantTag string
		wantErr error
	}{
		{
			name:    "valid_release",
			input:   `{"tag_name":"v0.9.1","name":"Release v0.9.1","draft":false}`,
			wantTag: "v0.9.1",
		},
		{
			name:    "extra_fields",
			input:   `{"tag_name":"v0.9.1","assets":[{"name":"pyry"}],"author":{"login":"x"}}`,
			wantTag: "v0.9.1",
		},
		{
			name:    "tag_without_v",
			input:   `{"tag_name":"0.9.1"}`,
			wantTag: "0.9.1",
		},
		{
			name:    "prerelease_tag",
			input:   `{"tag_name":"v0.10.0-rc1"}`,
			wantTag: "v0.10.0-rc1",
		},
		{
			name:    "malformed_json",
			input:   `not json`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "truncated_json",
			input:   `{"tag_name":`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "top_level_array",
			input:   `[1,2,3]`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "top_level_string",
			input:   `"hello"`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "missing_tag_name",
			input:   `{"name":"v0.9.1"}`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "empty_tag_name",
			input:   `{"tag_name":""}`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "wrong_type_tag_name",
			input:   `{"tag_name":42}`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "null_tag_name",
			input:   `{"tag_name":null}`,
			wantErr: ErrMalformedRelease,
		},
		{
			name:    "empty_input",
			input:   ``,
			wantErr: ErrMalformedRelease,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLatestRelease([]byte(tc.input))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.wantTag {
				t.Errorf("tag = %q, want %q", got, tc.wantTag)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		latest  string
		want    Comparison
		wantErr error
	}{
		{name: "equal_with_v", current: "v0.9.1", latest: "v0.9.1", want: Same},
		{name: "equal_no_v", current: "0.9.1", latest: "0.9.1", want: Same},
		{name: "mixed_prefix", current: "0.9.1", latest: "v0.9.1", want: Same},
		{name: "older_patch", current: "v0.9.0", latest: "v0.9.1", want: Older},
		{name: "older_minor", current: "v0.8.99", latest: "v0.9.0", want: Older},
		{name: "older_major", current: "v0.99.99", latest: "v1.0.0", want: Older},
		{name: "newer_patch", current: "v0.9.2", latest: "v0.9.1", want: Newer},
		{name: "newer_minor", current: "v0.10.0", latest: "v0.9.99", want: Newer},
		{name: "newer_major", current: "v1.0.0", latest: "v0.99.99", want: Newer},
		{name: "prerelease_current_stripped", current: "v0.10.0-rc1", latest: "v0.10.0", want: Same},
		{name: "prerelease_latest_stripped", current: "v0.10.0", latest: "v0.10.0-rc1", want: Same},
		{name: "build_metadata_stripped", current: "v0.10.0+build.5", latest: "v0.10.0", want: Same},
		{name: "dev_current", current: "dev", latest: "v0.9.1", wantErr: ErrInvalidVersion},
		{name: "dev_latest", current: "v0.9.1", latest: "dev", wantErr: ErrInvalidVersion},
		{name: "too_few_parts", current: "v0.9", latest: "v0.9.1", wantErr: ErrInvalidVersion},
		{name: "too_many_parts", current: "v0.9.1.2", latest: "v0.9.1", wantErr: ErrInvalidVersion},
		{name: "non_numeric", current: "v0.9.x", latest: "v0.9.1", wantErr: ErrInvalidVersion},
		{name: "empty_component", current: "v0..1", latest: "v0.9.1", wantErr: ErrInvalidVersion},
		{name: "negative", current: "v-1.0.0", latest: "v0.0.0", wantErr: ErrInvalidVersion},
		{name: "empty_string", current: "", latest: "v0.9.1", wantErr: ErrInvalidVersion},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CompareVersions(tc.current, tc.latest)
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
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}
