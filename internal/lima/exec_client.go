package lima

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Runner executes a single command. Production uses execRunner; tests inject a
// fake. Keeping this narrower than Client means the fake does not have to
// reimplement argument construction, so tests exercise the real arguments.
type Runner interface {
	Run(ctx context.Context, binary string, args []string, env []string) (stdout, stderr string, exitCode int, err error)
}

// execRunner runs commands with os/exec.
//
// No shell is involved: arguments are passed as separate tokens to
// exec.CommandContext, so an instance name or path can never be interpreted as
// shell syntax.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args []string, env []string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	// Lima must never read from a terminal. An explicitly empty stdin makes
	// any prompt fail fast instead of hanging Terraform.
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
			err = nil
		}
	}
	return stdout.String(), stderr.String(), exitCode, err
}

// ExecClient is the limactl command adapter.
type ExecClient struct {
	binary  string
	home    string
	env     map[string]string
	runner  Runner
	version Version
}

// Options configures an ExecClient.
type Options struct {
	// Binary is the limactl path or bare name. Empty means resolve "limactl"
	// from PATH.
	Binary string
	// Home is LIMA_HOME. Empty leaves Lima's own default in place.
	Home string
	// Env holds extra environment entries merged over the inherited env.
	Env map[string]string
	// Runner overrides command execution; nil selects os/exec.
	Runner Runner
}

// NewExecClient resolves the limactl binary and returns an adapter.
//
// It does not run a version check; call DetectVersion for that, so callers can
// distinguish "binary missing" from "binary too old" in their diagnostics.
func NewExecClient(opts Options) (*ExecClient, error) {
	binary := opts.Binary
	if binary == "" {
		binary = "limactl"
	}

	resolved, err := resolveBinary(binary)
	if err != nil {
		return nil, err
	}

	home := opts.Home
	if home != "" {
		home, err = ExpandPath(home)
		if err != nil {
			return nil, err
		}
	}

	runner := opts.Runner
	if runner == nil {
		runner = execRunner{}
	}

	return &ExecClient{
		binary: resolved,
		home:   home,
		env:    opts.Env,
		runner: runner,
	}, nil
}

// resolveBinary turns a configured binary into an executable path, or returns
// an error explaining precisely what was wrong.
func resolveBinary(binary string) (string, error) {
	if strings.ContainsRune(binary, os.PathSeparator) {
		expanded, err := ExpandPath(binary)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(expanded)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("no such file: %s", expanded)
			}
			return "", fmt.Errorf("cannot stat %s: %w", expanded, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory, not an executable", expanded)
		}
		if info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%s is not executable", expanded)
		}
		return expanded, nil
	}

	found, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("%q not found in PATH", binary)
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		return found, nil //nolint:nilerr // a relative resolved path still works
	}
	return abs, nil
}

// Binary returns the resolved limactl path.
func (c *ExecClient) Binary() string { return c.binary }

// Home returns the configured LIMA_HOME, which may be empty.
func (c *ExecClient) Home() string { return c.home }

// CachedVersion returns the version recorded by DetectVersion.
func (c *ExecClient) CachedVersion() Version { return c.version }

// run executes limactl and converts a non-zero exit into a CommandError.
func (c *ExecClient) run(ctx context.Context, args []string) (string, error) {
	env := buildEnv(os.Environ(), c.home, c.env)

	tflog.Debug(ctx, "executing limactl", map[string]any{
		"binary": c.binary,
		"args":   RedactArgs(args),
		"home":   c.home,
	})

	stdout, stderr, code, err := c.runner.Run(ctx, c.binary, args, env)
	if err != nil {
		// Context cancellation must surface as itself so callers can
		// distinguish a timeout from a Lima failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stdout, ctxErr
		}
		return stdout, &CommandError{
			Binary: c.binary,
			Args:   RedactArgs(args),
			Stdout: stdout,
			Stderr: stderr,
			Cause:  err,
		}
	}
	if code != 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stdout, ctxErr
		}
		cmdErr := &CommandError{
			Binary:   c.binary,
			Args:     RedactArgs(args),
			ExitCode: code,
			Stdout:   stdout,
			Stderr:   stderr,
		}
		tflog.Debug(ctx, "limactl failed", map[string]any{
			"args":      RedactArgs(args),
			"exit_code": code,
			"stderr":    cmdErr.Details(),
		})
		return stdout, cmdErr
	}
	return stdout, nil
}

// Version implements Client.
func (c *ExecClient) Version(ctx context.Context) (Version, error) {
	if c.version.Raw != "" {
		return c.version, nil
	}
	return c.DetectVersion(ctx)
}

// DetectVersion runs `limactl --version` and caches the result.
func (c *ExecClient) DetectVersion(ctx context.Context) (Version, error) {
	stdout, err := c.run(ctx, versionArgs())
	if err != nil {
		return Version{}, err
	}
	v, err := ParseVersion(stdout)
	if err != nil {
		return Version{}, err
	}
	c.version = v
	tflog.Debug(ctx, "detected Lima version", map[string]any{"version": v.String()})
	return v, nil
}

// Info implements Client.
func (c *ExecClient) Info(ctx context.Context) (HostInfo, error) {
	stdout, err := c.run(ctx, infoArgs())
	if err != nil {
		return HostInfo{}, err
	}
	return ParseHostInfo(strings.NewReader(stdout))
}

// List implements Client.
func (c *ExecClient) List(ctx context.Context) ([]Instance, error) {
	stdout, err := c.run(ctx, listArgs())
	if err != nil {
		// An empty LIMA_HOME warns on stderr but exits 0, so a real error
		// here is a real failure.
		return nil, err
	}
	return ParseInstancesString(stdout)
}

// Inspect implements Client.
//
// It lists everything and filters in Go rather than passing the name to Lima.
// Lima exits non-zero with a text-only message for an unknown name; listing
// makes absence a structural fact instead of a string match.
func (c *ExecClient) Inspect(ctx context.Context, name string) (Instance, error) {
	instances, err := c.List(ctx)
	if err != nil {
		return Instance{}, err
	}
	for _, inst := range instances {
		if inst.Name == name {
			return inst, nil
		}
	}
	return Instance{}, fmt.Errorf("%w: %q", ErrNotFound, name)
}

// Validate implements Client.
func (c *ExecClient) Validate(ctx context.Context, doc []byte) error {
	path, cleanup, err := writeTempDocument(doc, "validate")
	if err != nil {
		return err
	}
	defer cleanup()

	_, err = c.run(ctx, validateArgs(path))
	return err
}

// ResolveTemplate implements Client.
func (c *ExecClient) ResolveTemplate(ctx context.Context, ref string) (Template, error) {
	stdout, err := c.run(ctx, templateCopyArgs(ref))
	if err != nil {
		return Template{}, err
	}
	var t Template
	if err := yaml.Unmarshal([]byte(stdout), &t); err != nil {
		return Template{}, fmt.Errorf("parsing resolved template %q: %s", ref, sanitizeYAMLError(err))
	}
	return t, nil
}

// Create implements Client.
func (c *ExecClient) Create(ctx context.Context, req CreateRequest) error {
	path, cleanup, err := writeTempDocument(req.Document, "create")
	if err != nil {
		return err
	}
	// The temporary file is removed even if create fails or the context is
	// cancelled mid-flight.
	defer cleanup()

	_, err = c.run(ctx, createArgs(req.Name, path))
	if err != nil && IsAlreadyExists(err) {
		return fmt.Errorf("%w: %q", ErrAlreadyExists, req.Name)
	}
	return err
}

// Edit implements Client.
func (c *ExecClient) Edit(ctx context.Context, name string, req EditRequest) error {
	args, err := editArgs(name, req)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, args)
	return err
}

// Start implements Client.
func (c *ExecClient) Start(ctx context.Context, name string) error {
	_, err := c.run(ctx, startArgs(name))
	return err
}

// Stop implements Client.
func (c *ExecClient) Stop(ctx context.Context, name string) error {
	_, err := c.run(ctx, stopArgs(name))
	return err
}

// Delete implements Client.
func (c *ExecClient) Delete(ctx context.Context, name string) error {
	_, err := c.run(ctx, deleteArgs(name))
	if err != nil && IsProtected(err) {
		return fmt.Errorf("%w: %q", ErrProtected, name)
	}
	return err
}

// Protect implements Client.
func (c *ExecClient) Protect(ctx context.Context, name string) error {
	_, err := c.run(ctx, protectArgs(name))
	return err
}

// Unprotect implements Client.
func (c *ExecClient) Unprotect(ctx context.Context, name string) error {
	_, err := c.run(ctx, unprotectArgs(name))
	return err
}

// writeTempDocument writes a rendered configuration to a private temporary
// file and returns its path plus a cleanup function.
//
// os.CreateTemp creates with O_EXCL and mode 0600, so the file cannot be an
// attacker-planted symlink and cannot be read by other users. The document may
// contain provisioning scripts, so those permissions matter.
func writeTempDocument(doc []byte, purpose string) (string, func(), error) {
	f, err := os.CreateTemp("", "terraform-provider-lima-"+purpose+"-*.yaml")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating temporary Lima configuration: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }

	if _, err := f.Write(doc); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("writing temporary Lima configuration: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("closing temporary Lima configuration: %w", err)
	}
	return path, cleanup, nil
}
