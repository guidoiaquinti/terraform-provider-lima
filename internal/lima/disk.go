package lima

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Disk is a Lima additional disk, decoded from `limactl disk list --json`.
//
// Like the instance list, the output is NDJSON: one object per line with no
// enclosing array. See docs/development/lima-cli-contract.md §13.1.
type Disk struct {
	Name string `json:"name"`
	// SizeBytes is reported as a raw byte count.
	SizeBytes int64 `json:"size"`
	// Format is what Lima detects on disk, which is not necessarily what was
	// requested at creation: the vz driver requires raw images, so a qcow2
	// request becomes raw. See the CLI contract §13.3.
	Format string `json:"format"`
	Dir    string `json:"dir"`
	// Instance is the instance currently *running* with this disk attached,
	// or empty. A disk attached to a stopped instance reports empty here, so
	// this means "in use right now", not "attached to".
	Instance    string `json:"instance"`
	InstanceDir string `json:"instanceDir"`
	MountPoint  string `json:"mountPoint"`
}

// InUse reports whether a running instance currently holds the disk.
func (d Disk) InUse() bool { return d.Instance != "" }

// ErrDiskNotFound reports that no disk with the requested name exists.
var ErrDiskNotFound = errors.New("lima: disk not found")

// ErrDiskInUse reports that an operation was refused because a running
// instance holds the disk.
var ErrDiskInUse = errors.New("lima: disk is in use")

// ErrDiskExists reports a name collision.
var ErrDiskExists = errors.New("lima: disk already exists")

// Observed Lima 2.2.0 wording, used only as a fallback: in-use is normally
// detected structurally from the `instance` field.
var (
	diskInUseMarkers = []string{"in use by instance", "used by running instance"}
	// Backtick-anchored for the same reason as alreadyExistsMarkers: Lima says
	// "disk `data` already exists (...)" for a real collision, and says
	// "already exists" about unrelated things.
	diskExistsMarkers = []string{"` already exists"}
	diskShrinkMarkers = []string{"shrinking is currently unavailable"}
)

// IsDiskInUse reports whether err indicates a disk held by a running instance.
func IsDiskInUse(err error) bool {
	return errors.Is(err, ErrDiskInUse) || matchesAny(err, diskInUseMarkers)
}

// IsDiskExists reports whether err indicates a disk name collision.
func IsDiskExists(err error) bool {
	return errors.Is(err, ErrDiskExists) || matchesAny(err, diskExistsMarkers)
}

// IsDiskShrink reports whether err is Lima refusing to shrink a disk.
func IsDiskShrink(err error) bool { return matchesAny(err, diskShrinkMarkers) }

// IsDiskNotFound reports whether err indicates a missing disk.
func IsDiskNotFound(err error) bool { return errors.Is(err, ErrDiskNotFound) }

// ParseDisks decodes `limactl disk list --json` output.
func ParseDisks(r io.Reader) ([]Disk, error) {
	dec := json.NewDecoder(r)
	var out []Disk
	for {
		var d Disk
		err := dec.Decode(&d)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing limactl disk list output: %w", err)
		}
		if d.Name == "" {
			return nil, fmt.Errorf("parsing limactl disk list output: entry %d has no name", len(out))
		}
		out = append(out, d)
	}
	return out, nil
}

// ParseDisksString is a convenience wrapper over ParseDisks.
func ParseDisksString(s string) ([]Disk, error) { return ParseDisks(strings.NewReader(s)) }

// DiskClient is the disk half of the limactl surface.
//
// It is a separate interface from Client so a caller that only reads disks
// need not depend on the instance lifecycle, and so the fake can implement
// either independently.
type DiskClient interface {
	ListDisks(ctx context.Context) ([]Disk, error)
	InspectDisk(ctx context.Context, name string) (Disk, error)
	CreateDisk(ctx context.Context, req CreateDiskRequest) error
	ResizeDisk(ctx context.Context, name string, sizeBytes int64) error
	DeleteDisk(ctx context.Context, name string) error
}

// CreateDiskRequest describes one `limactl disk create`.
type CreateDiskRequest struct {
	Name      string
	SizeBytes int64
	// Format is passed straight through to --format. Empty leaves Lima's
	// default. Note that Lima may store a different format than requested.
	Format string
}

var _ DiskClient = (*ExecClient)(nil)

// ListDisks implements DiskClient.
func (c *ExecClient) ListDisks(ctx context.Context) ([]Disk, error) {
	stdout, err := c.run(ctx, diskListArgs())
	if err != nil {
		return nil, err
	}
	return ParseDisksString(stdout)
}

// InspectDisk implements DiskClient.
//
// As with instances, absence is resolved by listing everything and filtering
// in Go rather than by matching Lima's error text.
func (c *ExecClient) InspectDisk(ctx context.Context, name string) (Disk, error) {
	disks, err := c.ListDisks(ctx)
	if err != nil {
		return Disk{}, err
	}
	for _, d := range disks {
		if d.Name == name {
			return d, nil
		}
	}
	return Disk{}, fmt.Errorf("%w: %q", ErrDiskNotFound, name)
}

// CreateDisk implements DiskClient.
func (c *ExecClient) CreateDisk(ctx context.Context, req CreateDiskRequest) error {
	args, err := diskCreateArgs(req)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, args)
	if err != nil && IsDiskExists(err) {
		return fmt.Errorf("%w: %q", ErrDiskExists, req.Name)
	}
	return err
}

// ResizeDisk implements DiskClient.
func (c *ExecClient) ResizeDisk(ctx context.Context, name string, sizeBytes int64) error {
	_, err := c.run(ctx, diskResizeArgs(name, sizeBytes))
	if err != nil && IsDiskInUse(err) {
		return fmt.Errorf("%w: %q", ErrDiskInUse, name)
	}
	return err
}

// DeleteDisk implements DiskClient.
func (c *ExecClient) DeleteDisk(ctx context.Context, name string) error {
	_, err := c.run(ctx, diskDeleteArgs(name))
	if err != nil && IsDiskInUse(err) {
		return fmt.Errorf("%w: %q", ErrDiskInUse, name)
	}
	return err
}

// DiskService is the domain layer for disks.
//
// It mirrors Service: decide what to do from observed state, keep the
// Terraform layer free of command construction, and take the same keyed lock
// so concurrent operations on one disk cannot race.
type DiskService struct {
	client DiskClient
	locks  *KeyedMutex
}

// NewDiskService returns a disk lifecycle service.
func NewDiskService(c DiskClient, locks *KeyedMutex) *DiskService {
	if locks == nil {
		locks = NewKeyedMutex()
	}
	return &DiskService{client: c, locks: locks}
}

// Get returns one disk, or ErrDiskNotFound.
func (s *DiskService) Get(ctx context.Context, name string) (Disk, error) {
	return s.client.InspectDisk(ctx, name)
}

// List returns every disk in LIMA_HOME.
func (s *DiskService) List(ctx context.Context) ([]Disk, error) {
	return s.client.ListDisks(ctx)
}

// Exists reports whether a disk is registered.
func (s *DiskService) Exists(ctx context.Context, name string) (bool, error) {
	_, err := s.client.InspectDisk(ctx, name)
	if err == nil {
		return true, nil
	}
	if IsDiskNotFound(err) {
		return false, nil
	}
	return false, err
}

// Create registers a disk, refusing a name that is already taken so the caller
// can suggest an import.
func (s *DiskService) Create(ctx context.Context, req CreateDiskRequest) (Disk, error) {
	unlock, err := s.locks.Lock(ctx, DiskKey(req.Name))
	if err != nil {
		return Disk{}, err
	}
	defer unlock()

	exists, err := s.Exists(ctx, req.Name)
	if err != nil {
		return Disk{}, err
	}
	if exists {
		return Disk{}, fmt.Errorf("%w: %q", ErrDiskExists, req.Name)
	}

	tflog.Debug(ctx, "creating Lima disk", map[string]any{
		"name": req.Name, "size_bytes": req.SizeBytes, "format": req.Format,
	})
	if err := s.client.CreateDisk(ctx, req); err != nil {
		return Disk{}, err
	}
	return s.client.InspectDisk(ctx, req.Name)
}

// Resize grows a disk.
//
// Nothing happens when the disk is already the requested size, so re-applying
// an unchanged configuration is free. Shrinking is refused before Lima is
// asked, because its own refusal arrives only after the caller has committed
// to an apply.
func (s *DiskService) Resize(ctx context.Context, name string, sizeBytes int64) error {
	unlock, err := s.locks.Lock(ctx, DiskKey(name))
	if err != nil {
		return err
	}
	defer unlock()

	disk, err := s.client.InspectDisk(ctx, name)
	if err != nil {
		return err
	}
	if disk.SizeBytes == sizeBytes {
		return nil
	}
	if sizeBytes < disk.SizeBytes {
		return fmt.Errorf("cannot shrink disk %q from %s to %s: Lima does not support shrinking",
			name, FormatSize(disk.SizeBytes), FormatSize(sizeBytes))
	}
	// Checked up front so the caller gets a diagnostic naming the instance,
	// rather than Lima's terser message.
	if disk.InUse() {
		return fmt.Errorf("%w: %q is held by running instance %q", ErrDiskInUse, name, disk.Instance)
	}

	tflog.Debug(ctx, "resizing Lima disk", map[string]any{"name": name, "size_bytes": sizeBytes})
	return s.client.ResizeDisk(ctx, name, sizeBytes)
}

// Delete removes a disk. An already-absent disk is success.
func (s *DiskService) Delete(ctx context.Context, name string) error {
	unlock, err := s.locks.Lock(ctx, DiskKey(name))
	if err != nil {
		return err
	}
	defer unlock()

	disk, err := s.client.InspectDisk(ctx, name)
	if err != nil {
		if IsDiskNotFound(err) {
			return nil
		}
		return err
	}
	if disk.InUse() {
		return fmt.Errorf("%w: %q is held by running instance %q", ErrDiskInUse, name, disk.Instance)
	}

	tflog.Debug(ctx, "deleting Lima disk", map[string]any{"name": name})
	return s.client.DeleteDisk(ctx, name)
}
