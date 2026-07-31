// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Lima accepts Go-style byte sizes with IEC units: "4GiB", "8192MiB", "512M".
// The units below are the ones Lima's own templates use.
var sizeUnits = map[string]int64{
	"":    1,
	"B":   1,
	"K":   1 << 10,
	"KIB": 1 << 10,
	"M":   1 << 20,
	"MIB": 1 << 20,
	"G":   1 << 30,
	"GIB": 1 << 30,
	"T":   1 << 40,
	"TIB": 1 << 40,
}

var sizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]*)$`)

// ParseSize converts a Lima size string into bytes.
func ParseSize(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}
	m := sizePattern.FindStringSubmatch(trimmed)
	if m == nil {
		return 0, fmt.Errorf("%q is not a valid size (expected a number with an optional unit such as 4GiB or 8192MiB)", s)
	}
	unit, ok := sizeUnits[strings.ToUpper(m[2])]
	if !ok {
		return 0, fmt.Errorf("%q uses unknown unit %q (supported: B, KiB, MiB, GiB, TiB)", s, m[2])
	}
	value, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid size: %w", s, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("%q must not be negative", s)
	}
	return int64(value * float64(unit)), nil
}

// FormatSize renders bytes as the most compact exact IEC string.
//
// Only exact divisions are used, so 1.5GiB formats as 1536MiB rather than a
// lossy "1.5GiB" that would not round-trip.
func FormatSize(bytes int64) string {
	if bytes <= 0 {
		return "0B"
	}
	type unit struct {
		name string
		size int64
	}
	for _, u := range []unit{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
	} {
		if bytes >= u.size && bytes%u.size == 0 {
			return strconv.FormatInt(bytes/u.size, 10) + u.name
		}
	}
	return strconv.FormatInt(bytes, 10) + "B"
}

// gibiByte is the divisor for Lima's --memory and --disk flags, which take
// GiB rather than an IEC string.
const gibiByte = 1 << 30

// GiBFlagValue converts a byte count into the decimal GiB string that
// `limactl edit --memory` and `--disk` expect.
//
// Those flags are float32. The conversion is lossless for any MiB-granular
// size, because N MiB / 1024 is a dyadic rational and stays exact in float32
// while N < 2^24 (16 TiB) — verified against Lima 2.2.0 in the CLI contract
// §12.4.
//
// Rather than trust that silently, the value is converted back and compared.
// A size that cannot be represented exactly is refused, because resizing a VM
// to *almost* the requested amount would show up later as permanent drift.
func GiBFlagValue(bytes int64) (string, error) {
	if bytes <= 0 {
		return "", fmt.Errorf("size must be greater than zero")
	}

	gib := float64(bytes) / gibiByte
	// bitSize 32 yields the shortest decimal that round-trips through the
	// float32 the flag is parsed into.
	s := strconv.FormatFloat(gib, 'f', -1, 32)

	parsed, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return "", fmt.Errorf("converting %s to GiB: %w", FormatSize(bytes), err)
	}
	if int64(float64(float32(parsed))*gibiByte) != bytes {
		return "", fmt.Errorf(
			"%s cannot be passed to Lima exactly: its --memory and --disk flags take GiB as a 32-bit float, "+
				"and this value would be rounded. Use a size that is a whole number of MiB",
			FormatSize(bytes))
	}
	return s, nil
}

// Lima instance names become directory names, hostnames and socket paths, so
// they are restricted to a DNS-label-like form. Verified against Lima 2.2.0,
// which builds "lima-<name>" as the guest hostname.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// MaxNameLength bounds the name itself. The effective limit is usually the
// socket-path limit below, which depends on LIMA_HOME.
const MaxNameLength = 63

// ValidateName checks an instance name against Lima's constraints.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("instance name must not be empty")
	}
	if len(name) > MaxNameLength {
		return fmt.Errorf("instance name %q is %d characters; the maximum is %d", name, len(name), MaxNameLength)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("instance name %q is invalid: it must start with a letter or digit and contain only letters, digits, dots, dashes and underscores", name)
	}
	return nil
}

// unixPathMax is the limit Lima reports when a socket path is too long.
//
// Observed verbatim from Lima 2.2.0:
//
//	instance name `tfdisco` too long:
//	`<LIMA_HOME>/tfdisco/ssh.sock.1234567890123456` must be less than
//	UNIX_PATH_MAX=104 characters, but is 189
const unixPathMax = 104

// sshSockSuffix is "/<name>/ssh.sock." plus Lima's 16-digit suffix. The
// leading separator and the fixed filename are counted here; the name is added
// by the caller.
const sshSockSuffix = "/ssh.sock.1234567890123456"

// DefaultHomeDir is the directory Lima uses when LIMA_HOME is unset.
const DefaultHomeDir = ".lima"

// ResolveHome returns the directory Lima will actually use.
//
// An empty configured value means Lima's own default, `~/.lima`. Resolving it
// matters because the socket-path check below is only meaningful against a real
// path: skipping it whenever no home was configured meant skipping it for every
// default installation, which is most of them.
//
// An empty result means the home could not be determined at all, in which case
// callers must not guess.
func ResolveHome(configured string) string {
	if configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, DefaultHomeDir)
}

// ValidateNameForHome checks that an instance directory's socket path will fit
// inside UNIX_PATH_MAX for the given LIMA_HOME.
//
// Lima only discovers this at create time and fails mid-apply; checking it up
// front turns a confusing failure into an actionable plan-time error.
//
// An empty home is resolved to Lima's default rather than treated as "nothing to
// check", so the common case is covered.
func ValidateNameForHome(name, home string) error {
	home = ResolveHome(home)
	if home == "" {
		// No home could be determined, so there is no path to measure.
		return nil
	}
	total := len(home) + 1 + len(name) + len(sshSockSuffix)
	if total >= unixPathMax {
		excess := total - unixPathMax + 1
		return fmt.Errorf(
			"instance name %q is too long for LIMA_HOME %q: Lima builds a unix socket path of %d characters but the limit is %d; shorten the instance name by %d characters or use a shorter LIMA_HOME",
			name, home, total, unixPathMax, excess)
	}
	return nil
}

// KnownVMTypes are the values Lima 2.2.0 reported on the test host. Unknown
// values produce a warning, never an error, so a new Lima backend does not
// require a provider release.
var KnownVMTypes = []string{"qemu", "vz", "krunkit", "wsl2"}

// KnownArches are the architectures `limactl create --arch` documents.
var KnownArches = []string{"x86_64", "aarch64", "riscv64", "armv7l", "s390x", "ppc64le"}

// KnownProvisionModes are Lima's provisioning modes.
var KnownProvisionModes = []string{"system", "user", "boot", "dependency", "ansible", "data"}
