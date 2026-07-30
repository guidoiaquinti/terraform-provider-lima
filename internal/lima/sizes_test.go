package lima

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{input: "4GiB", want: 4 << 30},
		{input: "8192MiB", want: 8192 << 20},
		{input: "1TiB", want: 1 << 40},
		{input: "512KiB", want: 512 << 10},
		{input: "1024", want: 1024},
		{input: "100B", want: 100},
		// Lima's templates use both the long and short unit spellings.
		{input: "4G", want: 4 << 30},
		{input: "512M", want: 512 << 20},
		// Casing and internal spacing must not matter.
		{input: "4gib", want: 4 << 30},
		{input: "4 GiB", want: 4 << 30},
		{input: " 4GiB ", want: 4 << 30},
		{input: "1.5GiB", want: 1610612736},
		{input: "", wantErr: true},
		{input: "GiB", wantErr: true},
		{input: "4XB", wantErr: true},
		{input: "-4GiB", wantErr: true},
		{input: "four", wantErr: true},
		{input: "4GiB extra", wantErr: true},
	}

	for _, tc := range tests {
		name := tc.input
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSize(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSize(%q) = %d, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSize(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseSize(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		bytes int64
		want  string
	}{
		{4 << 30, "4GiB"},
		{8 << 30, "8GiB"},
		{1 << 40, "1TiB"},
		{512 << 20, "512MiB"},
		{1024, "1KiB"},
		{100, "100B"},
		{0, "0B"},
		// 1.5GiB is not an exact GiB multiple, so the exact MiB form is used
		// rather than a lossy decimal that would not round-trip.
		{1610612736, "1536MiB"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := FormatSize(tc.bytes); got != tc.want {
				t.Errorf("FormatSize(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
	}
}

func TestSizeRoundTrip(t *testing.T) {
	t.Parallel()

	// Formatting then parsing must be lossless, or normalisation would cause
	// a perpetual diff.
	for _, b := range []int64{1, 100, 1024, 4 << 30, 8192 << 20, 1610612736, 100 << 30} {
		s := FormatSize(b)
		back, err := ParseSize(s)
		if err != nil {
			t.Fatalf("ParseSize(FormatSize(%d)=%q) returned error: %v", b, s, err)
		}
		if back != b {
			t.Errorf("round trip of %d via %q gave %d", b, s, back)
		}
	}
}

func TestValidateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "simple", input: "dev"},
		{name: "with dash", input: "project-dev"},
		{name: "with underscore", input: "project_dev"},
		{name: "with dot", input: "project.dev"},
		{name: "digits", input: "vm1"},
		{name: "leading digit", input: "1vm"},
		{name: "empty", input: "", wantErr: true},
		{name: "leading dash", input: "-dev", wantErr: true},
		{name: "leading dot", input: ".dev", wantErr: true},
		{name: "space", input: "my vm", wantErr: true},
		{name: "slash", input: "a/b", wantErr: true},
		{name: "shell metacharacters", input: "dev; rm -rf /", wantErr: true},
		{name: "flag injection", input: "--help", wantErr: true},
		{name: "newline", input: "dev\nevil", wantErr: true},
		{name: "too long", input: strings.Repeat("a", 64), wantErr: true},
		{name: "at maximum length", input: strings.Repeat("a", 63)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateName(tc.input)
			if tc.wantErr != (err != nil) {
				t.Errorf("ValidateName(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestValidateNameForHome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inst    string
		home    string
		wantErr bool
	}{
		{
			// An unset home is the common case, not an exemption: Lima falls
			// back to ~/.lima, so the check has to run against that.
			name: "unset home is resolved rather than skipped",
			inst: "dev",
			home: "",
		},
		{
			name: "short home is fine",
			inst: "dev",
			home: "/tmp/lima",
		},
		{
			// The exact scenario that failed during discovery against real
			// Lima: a deep scratch directory pushed the socket path past
			// UNIX_PATH_MAX.
			name:    "deep home rejects an ordinary name",
			inst:    "tfdisco",
			home:    "/private/tmp/claude-501/-Users-alice-workspace-alice-terraform-provider-lima/18c09ee0-6347-49bf-b784-5e4f3785b801/scratchpad/disco/limahome",
			wantErr: true,
		},
		{
			name:    "long name with a moderate home",
			inst:    strings.Repeat("a", 60),
			home:    "/Users/alice/.lima",
			wantErr: true,
		},
		{
			name: "typical macOS home and name",
			inst: "project-dev",
			home: "/Users/alice/.lima",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateNameForHome(tc.inst, tc.home)
			if tc.wantErr != (err != nil) {
				t.Errorf("ValidateNameForHome(%q, %q) error = %v, wantErr %v", tc.inst, tc.home, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "too long") {
				t.Errorf("error should explain the length problem, got %q", err)
			}
		})
	}
}

func TestKnownValueTables(t *testing.T) {
	t.Parallel()

	// These come from `limactl info` and `limactl create --help` on Lima
	// 2.2.0; they gate warnings, never hard failures.
	if !slices.Contains(KnownVMTypes, "vz") || !slices.Contains(KnownVMTypes, "qemu") {
		t.Errorf("KnownVMTypes is missing a core backend: %v", KnownVMTypes)
	}
	if !slices.Contains(KnownArches, "aarch64") || !slices.Contains(KnownArches, "x86_64") {
		t.Errorf("KnownArches is missing a core architecture: %v", KnownArches)
	}
	if !slices.Contains(KnownProvisionModes, "system") || !slices.Contains(KnownProvisionModes, "user") {
		t.Errorf("KnownProvisionModes is missing a core mode: %v", KnownProvisionModes)
	}
}

func TestGiBFlagValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		bytes   int64
		want    string
		wantErr bool
	}{
		// The exact values measured against Lima 2.2.0; see the CLI contract
		// §12.4. Each of these produced a byte-exact round trip.
		{name: "8GiB", bytes: 8 << 30, want: "8"},
		{name: "16GiB", bytes: 16 << 30, want: "16"},
		{name: "512MiB", bytes: 512 << 20, want: "0.5"},
		{name: "1000MiB", bytes: 1000 << 20, want: "0.9765625"},
		{name: "7000MiB", bytes: 7000 << 20, want: "6.8359375"},
		{name: "1MiB", bytes: 1 << 20, want: "0.0009765625"},
		{name: "100GiB", bytes: 100 << 30, want: "100"},
		{name: "zero is rejected", bytes: 0, wantErr: true},
		{name: "negative is rejected", bytes: -1, wantErr: true},
		{
			// Not a whole MiB, so float32 cannot hold it exactly. Refusing
			// beats silently resizing to a slightly different value.
			name: "byte-granular size is refused", bytes: (1 << 30) + 1, wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := GiBFlagValue(tc.bytes)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GiBFlagValue(%d) = %q, want error", tc.bytes, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("GiBFlagValue(%d) returned error: %v", tc.bytes, err)
			}
			if got != tc.want {
				t.Errorf("GiBFlagValue(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
	}
}

func TestGiBFlagValueIsByteExact(t *testing.T) {
	t.Parallel()

	// Every whole-MiB size up to 16 GiB must convert without losing a byte,
	// which is the property that makes in-place resize safe.
	for mib := int64(1); mib <= 16*1024; mib++ {
		bytes := mib << 20
		s, err := GiBFlagValue(bytes)
		if err != nil {
			t.Fatalf("GiBFlagValue(%d MiB) returned error: %v", mib, err)
		}
		// Convert back the way Lima's float32 flag parser would.
		var f float32
		if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
			t.Fatalf("could not parse %q back: %v", s, err)
		}
		if got := int64(float64(f) * (1 << 30)); got != bytes {
			t.Fatalf("%d MiB round-tripped to %d bytes via %q, want %d", mib, got, s, bytes)
		}
	}
}

// ResolveHome decides which directory the socket-path check is measured against.
//
// The check previously did nothing when home was empty — which is precisely the
// default installation — so the failure it exists to pre-empt still arrived
// mid-apply from Lima itself for most users.
func TestResolveHome(t *testing.T) {
	t.Parallel()

	if got := ResolveHome("/tmp/lima"); got != "/tmp/lima" {
		t.Errorf("ResolveHome(%q) = %q, want it unchanged", "/tmp/lima", got)
	}

	// An unset home must resolve to Lima's own default rather than to nothing.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no user home directory on this host: %v", err)
	}
	if got, want := ResolveHome(""), filepath.Join(home, ".lima"); got != want {
		t.Errorf("ResolveHome(\"\") = %q, want %q", got, want)
	}
}

// A name that cannot fit under the *default* home must be rejected at plan time
// too, not only when the user configured an explicit home.
func TestValidateNameForHomeChecksTheDefaultHome(t *testing.T) {
	t.Parallel()

	if _, err := os.UserHomeDir(); err != nil {
		t.Skipf("no user home directory on this host: %v", err)
	}

	// Long enough that no plausible ~/.lima can accommodate it: the fixed
	// socket suffix alone is 26 characters, and the limit is 104.
	tooLong := strings.Repeat("a", 63)
	if err := ValidateNameForHome(tooLong, ""); err == nil {
		t.Error("a 63-character name passed the check against the default home; the check is not running")
	}

	// A short name must still be accepted, or every default install breaks.
	if err := ValidateNameForHome("dev", ""); err != nil {
		t.Errorf("ValidateNameForHome(\"dev\", \"\") = %v, want nil", err)
	}
}
