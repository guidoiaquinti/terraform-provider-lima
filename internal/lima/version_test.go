package lima

import "testing"

func TestParseVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    Version
		wantErr bool
	}{
		{
			// The exact output of `limactl --version` on Lima 2.2.0.
			name:  "limactl version line",
			input: "limactl version 2.2.0\n",
			want:  Version{Major: 2, Minor: 2, Patch: 0},
		},
		{
			name:  "bare version from limactl info",
			input: "2.2.0",
			want:  Version{Major: 2, Minor: 2, Patch: 0},
		},
		{
			name:  "leading v",
			input: "limactl version v1.0.3",
			want:  Version{Major: 1, Minor: 0, Patch: 3},
		},
		{
			name:  "development build metadata is preserved but ignored",
			input: "limactl version 2.2.0-12-gabcdef",
			want:  Version{Major: 2, Minor: 2, Patch: 0, Pre: "-12-gabcdef"},
		},
		{
			name:  "missing patch defaults to zero",
			input: "limactl version 3.1",
			want:  Version{Major: 3, Minor: 1, Patch: 0},
		},
		{
			name:  "trailing log lines are ignored",
			input: "limactl version 2.2.0\ntime=\"...\" level=warning msg=\"something\"\n",
			want:  Version{Major: 2, Minor: 2, Patch: 0},
		},
		{name: "empty", input: "", wantErr: true},
		{name: "no digits", input: "limactl version unknown", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseVersion(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) returned unexpected error: %v", tc.input, err)
			}
			if got.Major != tc.want.Major || got.Minor != tc.want.Minor || got.Patch != tc.want.Patch {
				t.Errorf("ParseVersion(%q) core = %s, want %s", tc.input, got.Core(), tc.want.Core())
			}
			if tc.want.Pre != "" && got.Pre != tc.want.Pre {
				t.Errorf("ParseVersion(%q) pre = %q, want %q", tc.input, got.Pre, tc.want.Pre)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	t.Parallel()

	v := func(ma, mi, pa int) Version { return Version{Major: ma, Minor: mi, Patch: pa} }

	tests := []struct {
		name string
		a, b Version
		want int
	}{
		{"equal", v(2, 2, 0), v(2, 2, 0), 0},
		{"major lower", v(1, 9, 9), v(2, 0, 0), -1},
		{"major higher", v(3, 0, 0), v(2, 9, 9), 1},
		{"minor lower", v(2, 1, 5), v(2, 2, 0), -1},
		{"patch higher", v(2, 2, 1), v(2, 2, 0), 1},
		// Multi-digit components must compare numerically, not lexically.
		{"double digit minor", v(2, 10, 0), v(2, 9, 0), 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.a.Compare(tc.b); got != tc.want {
				t.Errorf("%s.Compare(%s) = %d, want %d", tc.a.Core(), tc.b.Core(), got, tc.want)
			}
		})
	}
}

func TestCheckVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		version     Version
		wantErr     bool
		wantWarning bool
	}{
		{
			name:    "tested version is clean",
			version: Version{Major: 2, Minor: 2, Patch: 0},
		},
		{
			name:    "minimum supported version is accepted",
			version: MinimumVersion,
		},
		{
			name:    "older than minimum is rejected",
			version: Version{Major: 0, Minor: 9, Patch: 0},
			wantErr: true,
		},
		{
			// The 1.x line is deliberately unsupported, not merely untested:
			// nothing exercises it, so it must not be accepted with a warning.
			name:    "the 1.x line is rejected",
			version: Version{Major: 1, Minor: 9, Patch: 9},
			wantErr: true,
		},
		{
			// Forward compatibility: a newer Lima warns but must never block.
			name:        "newer than tested warns",
			version:     Version{Major: 99, Minor: 0, Patch: 0},
			wantWarning: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			warning, err := CheckVersion(tc.version)
			if tc.wantErr != (err != nil) {
				t.Fatalf("CheckVersion(%s) error = %v, wantErr %v", tc.version.Core(), err, tc.wantErr)
			}
			if tc.wantWarning != (warning != "") {
				t.Errorf("CheckVersion(%s) warning = %q, wantWarning %v", tc.version.Core(), warning, tc.wantWarning)
			}
		})
	}
}
