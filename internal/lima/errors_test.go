// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// limaLog renders a line the way Lima's logrus text formatter does.
func limaLog(level, msg string) string {
	return fmt.Sprintf("time=%q level=%s msg=%q\n", "2026-07-27T07:54:14+02:00", level, msg)
}

func TestCommandErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name:   "fatal line is preferred",
			stderr: limaLog("info", "Downloading image") + limaLog("fatal", "instance `dev` already exists"),
			want:   "instance `dev` already exists",
		},
		{
			name: "last fatal wins",
			stderr: limaLog("warning", "No instance matching nosuch found.") +
				limaLog("fatal", "unmatched instances"),
			want: "unmatched instances",
		},
		{
			name:   "falls back to the last line when nothing is fatal",
			stderr: limaLog("info", "first") + limaLog("info", "second"),
			want:   "second",
		},
		{
			name:   "plain non-logrus output passes through",
			stderr: "something went wrong\n",
			want:   "something went wrong",
		},
		{
			name:   "empty stderr yields an empty message",
			stderr: "",
			want:   "",
		},
		{
			name:   "escaped quotes inside the message are decoded",
			stderr: limaLog("fatal", `expected status "Running", got "Stopped"`),
			want:   `expected status "Running", got "Stopped"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &CommandError{Binary: "limactl", Args: []string{"create"}, ExitCode: 1, Stderr: tc.stderr}
			if got := e.Message(); got != tc.want {
				t.Errorf("Message() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommandErrorDropsDebugAndProgressNoise(t *testing.T) {
	t.Parallel()

	stderr := limaLog("debug", "internal detail") +
		limaLog("trace", "even more detail") +
		"1.23 GiB / 4.00 GiB [=====>-----] 30.00%\n" +
		limaLog("fatal", "real failure")

	e := &CommandError{Stderr: stderr}
	details := e.Details()

	for _, unwanted := range []string{"internal detail", "even more detail", "30.00%"} {
		if strings.Contains(details, unwanted) {
			t.Errorf("Details() should have dropped %q:\n%s", unwanted, details)
		}
	}
	if !strings.Contains(details, "real failure") {
		t.Errorf("Details() lost the real failure:\n%s", details)
	}
}

func TestCommandErrorDetailsAreBounded(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(limaLog("info", fmt.Sprintf("line %d", i)))
	}
	b.WriteString(limaLog("fatal", "the actual failure"))

	details := (&CommandError{Stderr: b.String()}).Details()
	if lines := strings.Count(details, "\n") + 1; lines > 25 {
		t.Errorf("Details() returned %d lines, want it capped near 20", lines)
	}
	// The most recent lines, including the failure, must survive truncation.
	if !strings.Contains(details, "the actual failure") {
		t.Errorf("truncation dropped the failure line:\n%s", details)
	}
}

func TestCommandErrorErrorString(t *testing.T) {
	t.Parallel()

	e := &CommandError{
		Binary:   "/opt/homebrew/bin/limactl",
		Args:     []string{"start", "--tty=false", "dev"},
		ExitCode: 1,
		Stderr:   limaLog("fatal", "boom"),
	}
	got := e.Error()

	for _, want := range []string{"limactl", "start", "exit status 1", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
}

func TestCommandErrorUnwrap(t *testing.T) {
	t.Parallel()

	cause := errors.New("permission denied")
	e := &CommandError{Binary: "limactl", Cause: cause}
	if !errors.Is(e, cause) {
		t.Error("errors.Is did not find the wrapped cause")
	}
}

func TestClassifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		notFound  bool
		protected bool
		exists    bool
	}{
		{
			name:     "sentinel not found",
			err:      fmt.Errorf("%w: %q", ErrNotFound, "dev"),
			notFound: true,
		},
		{
			// Real Lima 2.2.0 output for an unknown instance name.
			name: "not found from stderr",
			err: &CommandError{ExitCode: 1, Stderr: limaLog("warning", "No instance matching nosuch found.") +
				limaLog("fatal", "unmatched instances")},
			notFound: true,
		},
		{
			name:      "sentinel protected",
			err:       fmt.Errorf("%w: %q", ErrProtected, "dev"),
			protected: true,
		},
		{
			name: "protected from stderr",
			err: &CommandError{ExitCode: 1, Stderr: limaLog("fatal",
				"failed to delete instance `dev`: instance is protected to prohibit accidental removal (Hint: use `limactl unprotect`)")},
			protected: true,
		},
		{
			name:   "sentinel already exists",
			err:    fmt.Errorf("%w: %q", ErrAlreadyExists, "dev"),
			exists: true,
		},
		{
			name:   "already exists from stderr",
			err:    &CommandError{ExitCode: 1, Stderr: limaLog("fatal", "instance `dev` already exists")},
			exists: true,
		},
		{
			name: "an unrelated failure matches nothing",
			err:  &CommandError{ExitCode: 1, Stderr: limaLog("fatal", "disk full")},
		},
		{
			name: "a plain error matches nothing",
			err:  errors.New("boom"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsNotFound(tc.err); got != tc.notFound {
				t.Errorf("IsNotFound = %v, want %v", got, tc.notFound)
			}
			if got := IsProtected(tc.err); got != tc.protected {
				t.Errorf("IsProtected = %v, want %v", got, tc.protected)
			}
			if got := IsAlreadyExists(tc.err); got != tc.exists {
				t.Errorf("IsAlreadyExists = %v, want %v", got, tc.exists)
			}
		})
	}
}

func TestClassifiersAreCaseInsensitive(t *testing.T) {
	t.Parallel()

	// Lima's exact casing must not be load-bearing.
	err := &CommandError{ExitCode: 1, Stderr: limaLog("fatal", "Instance Is Protected")}
	if !IsProtected(err) {
		t.Error("IsProtected should match regardless of case")
	}
}

func TestSanitizeStderrLines(t *testing.T) {
	t.Parallel()

	input := limaLog("info", "Downloading") +
		limaLog("debug", "hidden") +
		limaLog("fatal", "failed") +
		"raw line\n" +
		"\n"

	got := SanitizeStderrLines(input)
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %#v", len(got), got)
	}
	if got[0].Level != "info" || got[0].Message != "Downloading" {
		t.Errorf("line 0 = %#v", got[0])
	}
	if got[1].Level != "fatal" || got[1].Message != "failed" {
		t.Errorf("line 1 = %#v", got[1])
	}
	if got[2].Message != "raw line" {
		t.Errorf("line 2 = %#v", got[2])
	}
}

func TestLogLineString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line LogLine
		want string
	}{
		// Info is the common case and reads better without a prefix.
		{LogLine{Level: "info", Message: "hello"}, "hello"},
		{LogLine{Message: "hello"}, "hello"},
		{LogLine{Level: "fatal", Message: "boom"}, "fatal: boom"},
		{LogLine{Level: "warning", Message: "careful"}, "warning: careful"},
	}
	for _, tc := range tests {
		if got := tc.line.String(); got != tc.want {
			t.Errorf("LogLine%+v.String() = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// Lima says "already exists" about things other than an instance name, and the
// marker used to match all of them.
//
// Concurrent first-time creates into an empty LIMA_HOME race on the shared SSH
// keypair, because Lima generates it by shelling out to ssh-keygen with no
// locking. The loser's stderr is:
//
//	failed to run [ssh-keygen ... -f <home>/_config/user]: "<home>/_config/user
//	already exists.\nOverwrite (y/n)? ": exit status 1
//
// A bare "already exists" substring matched that, so the provider reported a name
// collision and told the user to `terraform import` an instance that does not
// exist. Both messages were captured from Lima 2.2.0.
func TestIsAlreadyExistsDistinguishesTheKeypairRace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "genuine instance collision",
			stderr: "time=\"...\" level=fatal msg=\"instance `dev` already exists\"\n",
			want:   true,
		},
		{
			name: "shared keypair race is not a collision",
			stderr: "time=\"...\" level=fatal msg=\"failed to run [ssh-keygen -t ed25519 -q -N  -C lima " +
				"-f /tmp/lima/_config/user]: \\\"/tmp/lima/_config/user already exists.\\\\nOverwrite (y/n)? \\\": exit status 1\"\n",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := &CommandError{Binary: "limactl", ExitCode: 1, Stderr: tc.stderr}
			if got := IsAlreadyExists(err); got != tc.want {
				t.Errorf("IsAlreadyExists() = %v, want %v for %s", got, tc.want, tc.name)
			}
		})
	}
}

// The disk marker has the same shape, and Lima's disk collision message is
// "disk `name` already exists (...)", so it must survive the tightening.
func TestIsDiskExistsStillMatchesTheRealMessage(t *testing.T) {
	t.Parallel()

	err := &CommandError{
		Binary:   "limactl",
		ExitCode: 1,
		Stderr:   "time=\"...\" level=fatal msg=\"disk `data` already exists (`/tmp/lima/_disks/data`)\"\n",
	}
	if !IsDiskExists(err) {
		t.Error("IsDiskExists() = false for Lima's real disk collision message")
	}

	race := &CommandError{
		Binary:   "limactl",
		ExitCode: 1,
		Stderr:   "level=fatal msg=\"/tmp/lima/_config/user already exists.\"\n",
	}
	if IsDiskExists(race) {
		t.Error("IsDiskExists() = true for the shared keypair race")
	}
}

// A carriage return inside msg= must not survive into a diagnostic: it would
// overwrite whatever the terminal had already drawn on that line.
//
// This was uncovered by nothing. The hand-rolled unquoting always dropped \r, and
// when a strconv.Unquote fast path was added it silently started preserving it,
// because no test exercised the case.
func TestSanitizeStderrDropsCarriageReturns(t *testing.T) {
	t.Parallel()

	// Both a well-formed literal (the Unquote path) and one with trailing content
	// after the closing quote (the fallback scan).
	for _, stderr := range []string{
		"time=\"t\" level=fatal msg=\"first\\rsecond\"\n",
		"time=\"t\" level=fatal msg=\"first\\rsecond\" extra=1\n",
	} {
		lines := SanitizeStderrLines(stderr)
		if len(lines) != 1 {
			t.Fatalf("parsed %d lines from %q, want 1", len(lines), stderr)
		}
		if strings.Contains(lines[0].Message, "\r") {
			t.Errorf("message %q still contains a carriage return", lines[0].Message)
		}
		if !strings.Contains(lines[0].Message, "first") || !strings.Contains(lines[0].Message, "second") {
			t.Errorf("message %q lost content", lines[0].Message)
		}
	}
}
