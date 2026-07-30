package lima

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// CommandError describes a failed limactl invocation. It carries enough
// context to build an actionable diagnostic without leaking secrets: Args are
// redacted at construction time and Stderr is sanitised of Lima's logrus
// decoration.
type CommandError struct {
	Binary   string
	Args     []string
	ExitCode int
	Stdout   string
	Stderr   string
	Cause    error
}

func (e *CommandError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", e.Binary, strings.Join(e.Args, " "))
	if e.ExitCode != 0 {
		fmt.Fprintf(&b, ": exit status %d", e.ExitCode)
	}
	if msg := e.Message(); msg != "" {
		fmt.Fprintf(&b, ": %s", msg)
	}
	if e.Cause != nil && e.ExitCode == 0 {
		fmt.Fprintf(&b, ": %v", e.Cause)
	}
	return b.String()
}

func (e *CommandError) Unwrap() error { return e.Cause }

// Message returns the most useful single line Lima produced, with logrus
// timestamps and level prefixes removed. Lima writes its fatal reason last, so
// the final fatal/error line is preferred when present.
func (e *CommandError) Message() string {
	lines := SanitizeStderrLines(e.Stderr)
	if len(lines) == 0 {
		return ""
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Level == "fatal" || lines[i].Level == "error" {
			return lines[i].Message
		}
	}
	return lines[len(lines)-1].Message
}

// Details returns sanitised stderr suitable for embedding in a diagnostic,
// capped so a runaway log cannot dominate the output.
func (e *CommandError) Details() string {
	lines := SanitizeStderrLines(e.Stderr)
	const maxLines = 20
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.String())
	}
	return strings.Join(out, "\n")
}

// LogLine is one parsed line of Lima's stderr output.
type LogLine struct {
	Level   string
	Message string
}

func (l LogLine) String() string {
	if l.Level == "" || l.Level == "info" {
		return l.Message
	}
	return l.Level + ": " + l.Message
}

// Lima logs with logrus in text mode:
//
//	time="2026-07-27T07:54:14+02:00" level=info msg="Downloaded the image"
//
// The msg value is a Go-quoted string, so escaped quotes and newlines inside
// it must survive extraction intact.
var logrusLine = regexp.MustCompile(`^time="[^"]*"\s+level=(\w+)\s+msg=(.*)$`)

// SanitizeStderrLines parses Lima stderr into structured lines, dropping
// progress noise and decoding quoted messages. Lines that do not match Lima's
// log format are passed through verbatim.
func SanitizeStderrLines(stderr string) []LogLine {
	var out []LogLine
	for _, raw := range strings.Split(stderr, "\n") {
		line := strings.TrimRight(raw, "\r \t")
		if line == "" {
			continue
		}
		m := logrusLine.FindStringSubmatch(line)
		if m == nil {
			// Progress bars and carriage-return redraws are not useful.
			if strings.Contains(raw, "\r") || isProgressNoise(line) {
				continue
			}
			out = append(out, LogLine{Message: line})
			continue
		}
		level := m[1]
		msg := unquoteMessage(m[2])
		if level == "debug" || level == "trace" {
			continue
		}
		out = append(out, LogLine{Level: level, Message: msg})
	}
	return out
}

// unquoteMessage decodes the Go-quoted msg= value. Lima escapes embedded
// quotes, backticks are left as-is, and \n becomes a real newline.
//
// strconv.Unquote handles the well-formed case, which is nearly all of them. The
// manual scan below remains as a fallback because Lima's msg= value is not always
// a complete Go string literal — a truncated log line, or trailing key=value
// pairs after the closing quote, both make Unquote fail where taking everything
// up to the closing quote still yields the message.
func unquoteMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	if decoded, err := strconv.Unquote(s); err == nil {
		// Unquote keeps a carriage return; the manual scan below drops it and so
		// must this path. A stray \r in a diagnostic overwrites the line the
		// terminal has already drawn.
		return strings.ReplaceAll(decoded, "\r", "")
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			break
		}
		if c == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				// drop
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isProgressNoise(line string) bool {
	// e.g. "1.23 GiB / 4.00 GiB [====>-----] 30.00%"
	return strings.Contains(line, "] ") && strings.Contains(line, "%") &&
		strings.Contains(line, "[")
}

// ErrNotFound reports that Lima has no instance with the requested name.
var ErrNotFound = errors.New("lima: instance not found")

// ErrProtected reports that an operation was refused because the instance is
// protected against accidental removal.
var ErrProtected = errors.New("lima: instance is protected")

// ErrAlreadyExists reports that an instance with the requested name is already
// registered in LIMA_HOME.
var ErrAlreadyExists = errors.New("lima: instance already exists")

// Observed Lima 2.2.0 messages. These are matched only as a fallback: the
// primary detection paths are structural (absence from a full list, or the
// `protected` field), so a change in Lima's wording degrades diagnostics
// rather than breaking behaviour.
var (
	notFoundMarkers = []string{
		"unmatched instances",
		"no instance matching",
	}
	protectedMarkers = []string{
		"instance is protected",
	}
	// The backtick matters. Lima names the object in a genuine collision —
	// "instance `dev` already exists" — while its other "already exists" messages
	// do not. In particular, concurrent first-time creates race on the shared SSH
	// keypair and the loser reports
	// "<home>/_config/user already exists. Overwrite (y/n)?", which a bare
	// "already exists" marker matched, so the provider claimed a name collision
	// and told the user to import an instance that did not exist.
	alreadyExistsMarkers = []string{
		"` already exists",
	}
)

func matchesAny(err error, markers []string) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	hay := strings.ToLower(ce.Stderr)
	for _, m := range markers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

// IsNotFound reports whether err indicates a missing instance.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || matchesAny(err, notFoundMarkers)
}

// IsProtected reports whether err indicates a protection refusal.
func IsProtected(err error) bool {
	return errors.Is(err, ErrProtected) || matchesAny(err, protectedMarkers)
}

// IsAlreadyExists reports whether err indicates a name collision.
func IsAlreadyExists(err error) bool {
	return errors.Is(err, ErrAlreadyExists) || matchesAny(err, alreadyExistsMarkers)
}
