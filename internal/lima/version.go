package lima

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinimumVersion is the oldest Lima release this provider supports. The
// provider targets the 2.x line only: 1.x is never exercised in CI or by hand,
// so accepting it would claim support that nothing verifies. It is rejected
// outright rather than accepted with a warning, because a silent acceptance is
// the more expensive failure — it surfaces as a confusing mid-apply error
// instead of a clear one at configure time.
var MinimumVersion = Version{Major: 2, Minor: 0, Patch: 0}

// MaxTestedVersion is the newest Lima release the provider has been exercised
// against. Newer releases produce a warning, never an error, so a Lima upgrade
// does not break a working configuration.
var MaxTestedVersion = Version{Major: 2, Minor: 2, Patch: 0}

// Version is a parsed Lima version.
type Version struct {
	Major int
	Minor int
	Patch int
	// Pre holds any suffix after the semver core, e.g. "-12-gabcdef" for a
	// development build. It is preserved for diagnostics and ignored when
	// comparing.
	Pre string
	// Raw is the exact string limactl reported.
	Raw string
}

func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Core renders just the numeric portion.
func (v Version) Core() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare orders v against o by major, minor then patch. Pre-release suffixes
// are ignored: a development build of 2.2.0 is treated as 2.2.0, which is the
// forgiving choice for a tool the user installed deliberately.
func (v Version) Compare(o Version) int {
	switch {
	case v.Major != o.Major:
		return sign(v.Major - o.Major)
	case v.Minor != o.Minor:
		return sign(v.Minor - o.Minor)
	case v.Patch != o.Patch:
		return sign(v.Patch - o.Patch)
	}
	return 0
}

// AtLeast reports whether v is o or newer.
func (v Version) AtLeast(o Version) bool { return v.Compare(o) >= 0 }

// NewerThan reports whether v is strictly newer than o.
func (v Version) NewerThan(o Version) bool { return v.Compare(o) > 0 }

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// limactl --version prints exactly:
//
//	limactl version 2.2.0
//
// Development builds append metadata (2.2.0-12-gabcdef). The version may also
// arrive bare (from `limactl info`, which reports "2.2.0").
var versionPattern = regexp.MustCompile(`(?m)v?(\d+)\.(\d+)(?:\.(\d+))?([0-9A-Za-z.\-+]*)`)

// ParseVersion extracts a Lima version from limactl output. It tolerates the
// "limactl version " prefix, a leading "v", a missing patch component and
// trailing build metadata.
func ParseVersion(s string) (Version, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Version{}, fmt.Errorf("empty version output")
	}
	// Only consider the first line; limactl may log warnings afterwards.
	if i := strings.IndexByte(trimmed, '\n'); i >= 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	m := versionPattern.FindStringSubmatch(trimmed)
	if m == nil {
		return Version{}, fmt.Errorf("no version number found in %q", trimmed)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return Version{}, fmt.Errorf("invalid major version in %q: %w", trimmed, err)
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return Version{}, fmt.Errorf("invalid minor version in %q: %w", trimmed, err)
	}
	patch := 0
	if m[3] != "" {
		patch, err = strconv.Atoi(m[3])
		if err != nil {
			return Version{}, fmt.Errorf("invalid patch version in %q: %w", trimmed, err)
		}
	}
	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Pre:   m[4],
		Raw:   trimmed,
	}, nil
}
