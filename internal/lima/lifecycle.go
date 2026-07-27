package lima

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Service is the domain layer. It owns the decisions about *when* to invoke a
// Lima command, based on observed state, and keeps that logic out of both the
// Terraform layer above and the command adapter below.
//
// It deliberately depends on nothing from the Terraform framework except
// tflog, so it can be tested with a fake Client and no Terraform machinery.
type Service struct {
	client Client
	locks  *KeyedMutex
	poll   PollOptions
}

// ServiceOption customises a Service.
type ServiceOption func(*Service)

// WithPollOptions overrides the polling cadence, primarily for tests.
func WithPollOptions(o PollOptions) ServiceOption {
	return func(s *Service) { s.poll = o }
}

// WithLocks shares a KeyedMutex across services.
func WithLocks(m *KeyedMutex) ServiceOption {
	return func(s *Service) { s.locks = m }
}

// NewService returns a lifecycle service over the given client.
func NewService(c Client, opts ...ServiceOption) *Service {
	s := &Service{client: c, locks: NewKeyedMutex()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Client exposes the underlying adapter for read-only callers such as data
// sources, which need no locking.
func (s *Service) Client() Client { return s.client }

// Get returns the current state of an instance, or ErrNotFound.
func (s *Service) Get(ctx context.Context, name string) (Instance, error) {
	return s.client.Inspect(ctx, name)
}

// Exists reports whether an instance is registered in LIMA_HOME.
func (s *Service) Exists(ctx context.Context, name string) (bool, error) {
	_, err := s.client.Inspect(ctx, name)
	if err == nil {
		return true, nil
	}
	if IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// CreateParams describes a full create-and-optionally-start operation.
type CreateParams struct {
	Name string
	// Document is the rendered Lima YAML.
	Document []byte
	// Start requests that the instance be running when the call returns.
	Start bool
	// Protect requests protection be applied after creation.
	Protect bool
	// Validate runs `limactl validate` on the document first. Enabled in
	// normal operation; tests may disable it.
	Validate bool
}

// CreateResult reports what happened, including whether Lima registered the
// instance despite an overall failure.
type CreateResult struct {
	Instance Instance
	// Registered is true once Lima has an instance directory for the name.
	// On a failed create-then-start this tells the caller that state must be
	// written so Terraform can manage (and destroy) the partial instance.
	Registered bool
}

// Create registers an instance and brings it to the requested state.
//
// Partial failure is handled explicitly: if create succeeds but start fails,
// the instance is left in place and Registered is true. The provider then
// writes state so a subsequent apply or destroy can recover. Nothing is
// auto-deleted, because deleting a VM the user may have data in is not a
// recovery a provider should make on its own.
func (s *Service) Create(ctx context.Context, p CreateParams) (CreateResult, error) {
	var res CreateResult

	unlock, err := s.locks.Lock(ctx, InstanceKey(p.Name))
	if err != nil {
		return res, err
	}
	defer unlock()

	// Collision check before doing anything destructive-adjacent, so the
	// caller can emit an import hint rather than Lima's terse message.
	exists, err := s.Exists(ctx, p.Name)
	if err != nil {
		return res, err
	}
	if exists {
		return res, fmt.Errorf("%w: %q", ErrAlreadyExists, p.Name)
	}

	if p.Validate {
		if err := s.client.Validate(ctx, p.Document); err != nil {
			return res, err
		}
	}

	tflog.Debug(ctx, "creating Lima instance", map[string]any{"name": p.Name})
	if err := s.client.Create(ctx, CreateRequest{Name: p.Name, Document: p.Document}); err != nil {
		// Create may fail after Lima registered the directory. Check, so the
		// caller learns whether cleanup is needed.
		if reg, checkErr := s.Exists(ctx, p.Name); checkErr == nil && reg {
			res.Registered = true
		}
		return res, err
	}
	res.Registered = true

	// Create leaves the instance stopped; wait for it to settle there so a
	// start = false resource does not return while Lima is still writing.
	if err := s.waitForStatus(ctx, p.Name, "creation", StatusStopped, StatusRunning); err != nil {
		return res, err
	}

	if p.Start {
		if err := s.startLocked(ctx, p.Name); err != nil {
			return res, err
		}
	}

	if p.Protect {
		if err := s.client.Protect(ctx, p.Name); err != nil {
			return res, err
		}
	}

	inst, err := s.client.Inspect(ctx, p.Name)
	if err != nil {
		return res, err
	}
	res.Instance = inst
	return res, nil
}

// EnsureRunning starts the instance if it is not already running.
func (s *Service) EnsureRunning(ctx context.Context, name string) error {
	unlock, err := s.locks.Lock(ctx, InstanceKey(name))
	if err != nil {
		return err
	}
	defer unlock()
	return s.startLocked(ctx, name)
}

func (s *Service) startLocked(ctx context.Context, name string) error {
	inst, err := s.client.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if inst.Status() == StatusRunning {
		tflog.Debug(ctx, "instance already running, nothing to start", map[string]any{"name": name})
		return nil
	}

	tflog.Debug(ctx, "starting Lima instance", map[string]any{"name": name})
	if err := s.client.Start(ctx, name); err != nil {
		return err
	}
	return s.waitForStatus(ctx, name, "start", StatusRunning)
}

// EnsureStopped stops the instance if it is running.
//
// Lima errors when asked to stop an already-stopped instance
// ("expected status `Running`, got `Stopped`"), so the status check here is
// required for correctness, not just efficiency.
func (s *Service) EnsureStopped(ctx context.Context, name string) error {
	unlock, err := s.locks.Lock(ctx, InstanceKey(name))
	if err != nil {
		return err
	}
	defer unlock()
	return s.stopLocked(ctx, name)
}

func (s *Service) stopLocked(ctx context.Context, name string) error {
	inst, err := s.client.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if inst.Status() == StatusStopped {
		tflog.Debug(ctx, "instance already stopped, nothing to stop", map[string]any{"name": name})
		return nil
	}

	tflog.Debug(ctx, "stopping Lima instance", map[string]any{"name": name})
	if err := s.client.Stop(ctx, name); err != nil {
		return err
	}
	return s.waitForStatus(ctx, name, "stop", StatusStopped)
}

// ResizeParams describes an in-place resource change.
type ResizeParams struct {
	Name string
	// AdditionalDisks is the configured attachment list. Nil means the
	// attribute is unmanaged. Unlike mounts, this needs no merge: Lima
	// contributes no disks of its own, so the declared list is the whole
	// list.
	AdditionalDisks *[]AdditionalDisk
	// Desired holds the target values. A zero field means unchanged.
	Desired EditRequest

	// Mounts and PortForwards are the *configured* lists, not the merged
	// ones. Nil means the attribute is unmanaged and must be left alone.
	// Previous* is what Terraform state recorded, and is used to work out
	// which resolved entries the base template contributed.
	Mounts               *[]Mount
	PreviousMounts       []Mount
	PortForwards         *[]PortForward
	PreviousPortForwards []PortForward
	// WantRunning is the desired final state. It is honoured independently
	// of whether the instance was running before, so a resize combined with
	// a start/stop change is a single coherent operation.
	WantRunning bool
}

// RestartAfterEditError reports that the configuration change was applied but
// the instance could not be brought back up.
//
// This is the dangerous case the resize path has to be explicit about: the
// user's requested change *did* take effect, so retrying the whole operation
// is safe, but the VM is down and they need to know that rather than seeing a
// generic start failure.
type RestartAfterEditError struct {
	Name  string
	Cause error
}

func (e *RestartAfterEditError) Error() string {
	return fmt.Sprintf("instance %q was reconfigured successfully but could not be restarted: %v", e.Name, e.Cause)
}

func (e *RestartAfterEditError) Unwrap() error { return e.Cause }

// Resize applies an in-place resource change.
//
// Lima refuses to edit a running instance, so the sequence is:
//
//  1. inspect and record whether the instance is running
//  2. stop it if necessary
//  3. apply the edit
//  4. restart it only when the desired state calls for it
//
// If the edit itself fails, the instance is left stopped rather than being
// restarted with the old configuration: a failed edit may have been rejected
// for a reason the user must see, and silently bringing the VM back up would
// hide that the change did not apply. The error says so.
//
// Verified against Lima 2.2.0; see docs/development/lima-cli-contract.md §12.
func (s *Service) Resize(ctx context.Context, p ResizeParams) error {
	// Mounts and port forwards live on ResizeParams rather than on Desired,
	// because computing the list Lima needs requires the resolved
	// configuration. They must be considered here too, or a mounts-only
	// change would return before the merge below ever ran.
	if p.Desired.IsEmpty() && p.Mounts == nil && p.PortForwards == nil && p.AdditionalDisks == nil {
		// Nothing to change; still reconcile the run state, because a plan
		// may combine a no-op resize with a start/stop.
		return s.reconcileRunState(ctx, p.Name, p.WantRunning)
	}

	unlock, err := s.locks.Lock(ctx, InstanceKey(p.Name))
	if err != nil {
		return err
	}
	defer unlock()

	inst, err := s.client.Inspect(ctx, p.Name)
	if err != nil {
		return err
	}

	// Compare against what Lima actually reports, not against Terraform
	// state. State can be silent about a value (an imported instance has no
	// recorded cpus), and restarting a VM to apply a change it already has
	// would be pure downtime for nothing.
	desired := filterToActualChanges(p.Desired, inst)

	// Mounts and port forwards replace an already-merged list, so the entries
	// the base template contributed have to be carried across explicitly.
	if p.Mounts != nil {
		merged := mergeMounts(inst.Config.Mounts, p.PreviousMounts, *p.Mounts)
		if !sameMounts(merged, inst.Config.Mounts) {
			desired.Mounts = &merged
		}
	}
	if p.PortForwards != nil {
		merged := mergePortForwards(inst.Config.PortForwards, p.PreviousPortForwards, *p.PortForwards)
		if !samePortForwards(merged, inst.Config.PortForwards) {
			desired.PortForwards = &merged
		}
	}

	if p.AdditionalDisks != nil && !sameDisks(*p.AdditionalDisks, inst.Config.AdditionalDisks) {
		disks := *p.AdditionalDisks
		desired.AdditionalDisks = &disks
	}

	if desired.IsEmpty() {
		tflog.Debug(ctx, "instance already matches the requested resources, skipping edit",
			map[string]any{"name": p.Name})
		return s.reconcileRunStateLocked(ctx, p.Name, p.WantRunning)
	}

	wasRunning := inst.Status() == StatusRunning

	if wasRunning {
		tflog.Debug(ctx, "stopping instance to apply an in-place resize", map[string]any{
			"name": p.Name,
		})
		if err := s.stopLocked(ctx, p.Name); err != nil {
			return fmt.Errorf("stopping instance %q before reconfiguring it: %w", p.Name, err)
		}
	}

	tflog.Debug(ctx, "editing Lima instance", map[string]any{
		"name":         p.Name,
		"cpus":         desired.CPUs,
		"memory_bytes": desired.MemoryBytes,
		"disk_bytes":   desired.DiskBytes,
	})
	if err := s.client.Edit(ctx, p.Name, desired); err != nil {
		return err
	}

	if p.WantRunning {
		if err := s.startLocked(ctx, p.Name); err != nil {
			return &RestartAfterEditError{Name: p.Name, Cause: err}
		}
	}
	return nil
}

// reconcileRunState brings the instance to the requested run state.
func (s *Service) reconcileRunState(ctx context.Context, name string, wantRunning bool) error {
	if wantRunning {
		return s.EnsureRunning(ctx, name)
	}
	return s.EnsureStopped(ctx, name)
}

// reconcileRunStateLocked is reconcileRunState for callers that already hold
// the instance lock.
func (s *Service) reconcileRunStateLocked(ctx context.Context, name string, wantRunning bool) error {
	if wantRunning {
		return s.startLocked(ctx, name)
	}
	return s.stopLocked(ctx, name)
}

// SetProtection applies or clears protection, skipping the command when the
// instance is already in the requested state.
func (s *Service) SetProtection(ctx context.Context, name string, want bool) error {
	unlock, err := s.locks.Lock(ctx, InstanceKey(name))
	if err != nil {
		return err
	}
	defer unlock()

	inst, err := s.client.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if inst.Protected == want {
		return nil
	}
	if want {
		return s.client.Protect(ctx, name)
	}
	return s.client.Unprotect(ctx, name)
}

// Delete removes an instance and waits for it to disappear.
//
// An already-absent instance is success. A protected instance fails with
// ErrProtected and is never silently unprotected: protection is an explicit
// user statement that the VM must not be destroyed accidentally, and a
// provider that worked around it would make the flag meaningless.
func (s *Service) Delete(ctx context.Context, name string) error {
	unlock, err := s.locks.Lock(ctx, InstanceKey(name))
	if err != nil {
		return err
	}
	defer unlock()

	inst, err := s.client.Inspect(ctx, name)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return err
	}
	if inst.Protected {
		return fmt.Errorf("%w: %q", ErrProtected, name)
	}

	tflog.Debug(ctx, "deleting Lima instance", map[string]any{"name": name})
	if err := s.client.Delete(ctx, name); err != nil {
		return err
	}
	return s.waitForAbsence(ctx, name)
}

// waitForStatus polls until the instance reports one of want.
func (s *Service) waitForStatus(ctx context.Context, name, operation string, want ...Status) error {
	labels := make([]string, 0, len(want))
	for _, w := range want {
		labels = append(labels, string(w))
	}
	wantLabel := joinOr(labels)

	return Poll(ctx, s.poll, fmt.Sprintf("%s of instance %q", operation, name), wantLabel,
		func(ctx context.Context) (PollResult, error) {
			inst, err := s.client.Inspect(ctx, name)
			if err != nil {
				if IsNotFound(err) {
					// Not yet registered; keep waiting rather than failing,
					// since create is asynchronous from the filesystem's view.
					return PollResult{Observed: "absent"}, nil
				}
				return PollResult{}, err
			}
			st := inst.Status()
			if st == StatusBroken {
				return PollResult{}, fmt.Errorf("instance %q entered the %q state during %s", name, inst.RawStatus, operation)
			}
			for _, w := range want {
				if st == w {
					return PollResult{Done: true, Observed: string(st)}, nil
				}
			}
			return PollResult{Observed: string(st)}, nil
		})
}

// waitForAbsence polls until the instance is gone.
func (s *Service) waitForAbsence(ctx context.Context, name string) error {
	return Poll(ctx, s.poll, fmt.Sprintf("deletion of instance %q", name), "absent",
		func(ctx context.Context) (PollResult, error) {
			inst, err := s.client.Inspect(ctx, name)
			if err != nil {
				if IsNotFound(err) {
					return PollResult{Done: true, Observed: "absent"}, nil
				}
				return PollResult{}, err
			}
			return PollResult{Observed: string(inst.Status())}, nil
		})
}

func joinOr(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	out := ""
	for i, it := range items {
		switch {
		case i == 0:
			out = it
		case i == len(items)-1:
			out += " or " + it
		default:
			out += ", " + it
		}
	}
	return out
}

// CheckVersion validates a detected Lima version against the supported range.
//
// It returns a fatal error for versions that are too old, and a non-empty
// warning string for versions newer than the tested maximum. Splitting these
// keeps the policy in one place instead of scattering version checks through
// resource code.
func CheckVersion(v Version) (warning string, err error) {
	if !v.AtLeast(MinimumVersion) {
		return "", fmt.Errorf(
			"detected Lima %s, which is older than the minimum supported version %s; upgrade Lima to %s or newer",
			v.String(), MinimumVersion.Core(), MinimumVersion.Core())
	}
	if v.NewerThan(MaxTestedVersion) {
		return fmt.Sprintf(
			"Lima %s is newer than the most recent version this provider was tested against (%s). "+
				"The provider will proceed; report any incompatibility you encounter.",
			v.String(), MaxTestedVersion.Core()), nil
	}
	return "", nil
}

// DefaultTimeouts are the fallbacks documented in the provider schema.
var DefaultTimeouts = struct {
	Create, Update, Delete, Read time.Duration
}{
	Create: 30 * time.Minute,
	Update: 20 * time.Minute,
	Delete: 20 * time.Minute,
	Read:   2 * time.Minute,
}

// IsTimeout reports whether err came from Poll running out of time.
func IsTimeout(err error) bool {
	var te *TimeoutError
	return errors.As(err, &te)
}

// filterToActualChanges narrows an edit request to the fields that genuinely
// differ from what Lima currently reports.
//
// Terraform state can legitimately be silent about a value: after an import,
// `cpus` is unset even though the VM plainly has a CPU count. Asking for
// "4 CPUs" when the instance already has 4 would otherwise stop and restart
// the VM to apply nothing. Comparing against observed reality instead of
// against state makes Resize idempotent.
func filterToActualChanges(req EditRequest, inst Instance) EditRequest {
	if req.CPUs == inst.CPUs {
		req.CPUs = 0
	}
	if req.MemoryBytes == inst.MemoryBytes {
		req.MemoryBytes = 0
	}
	if req.DiskBytes == inst.DiskBytes {
		req.DiskBytes = 0
	}
	return req
}

// mergeMounts computes the mount list to write back for an in-place change.
//
// `--set .mounts` replaces the already-merged list, and base merging does not
// run again on edit (CLI contract §12.8). So the new list must be the newly
// configured mounts followed by whatever the base template contributed —
// which is the resolved list minus the mounts that were previously
// configured.
//
// Previously configured entries are matched by **location alone**, which is
// Lima's own notion of mount identity (its --mount flag warns against
// specifying directories that overlap existing mounts). Matching on the full
// tuple would break in two ways:
//
//   - if the mount was modified outside Terraform, the old entry would not be
//     recognised and the location would end up mounted twice;
//   - if it was removed outside Terraform, an exact match would fail and
//     block even unrelated changes.
//
// With location matching, a mount that drifted is simply restored by the next
// apply.
func mergeMounts(resolved []MountView, previous, desired []Mount) []Mount {
	keep := make([]MountView, len(resolved))
	copy(keep, resolved)

	for _, prev := range previous {
		if idx := indexOfMountLocation(keep, prev.Location); idx >= 0 {
			keep = append(keep[:idx], keep[idx+1:]...)
		}
	}

	// Declared mounts first, then template-contributed ones, matching the
	// order `limactl create` produces.
	out := make([]Mount, 0, len(desired)+len(keep))
	out = append(out, desired...)
	for _, m := range keep {
		// A location the user now declares must not also survive as an
		// inherited entry, or it would be mounted twice.
		if indexOfMountLocation2(desired, m.Location) >= 0 {
			continue
		}
		out = append(out, Mount(m))
	}
	return out
}

func indexOfMountLocation(list []MountView, location string) int {
	for i, m := range list {
		if samePath(m.Location, location) {
			return i
		}
	}
	return -1
}

func indexOfMountLocation2(list []Mount, location string) int {
	for i, m := range list {
		if samePath(m.Location, location) {
			return i
		}
	}
	return -1
}

// mergePortForwards is mergeMounts for port forwards.
//
// Identity here is the guest port and protocol: the host port is exactly the
// thing a user is most likely to change, so including it would leave the old
// forward behind.
func mergePortForwards(resolved []PortForwardView, previous, desired []PortForward) []PortForward {
	keep := make([]PortForwardView, len(resolved))
	copy(keep, resolved)

	for _, prev := range previous {
		if idx := indexOfForward(keep, prev.GuestPort, prev.Proto); idx >= 0 {
			keep = append(keep[:idx], keep[idx+1:]...)
		}
	}

	out := make([]PortForward, 0, len(desired)+len(keep))
	out = append(out, desired...)
	for _, p := range keep {
		if indexOfForward2(desired, p.GuestPort, p.Proto) >= 0 {
			continue
		}
		out = append(out, PortForward{
			GuestPort: p.GuestPort,
			HostPort:  p.HostPort,
			Proto:     p.Proto,
			GuestIP:   p.GuestIP,
			HostIP:    p.HostIP,
		})
	}
	return out
}

func normalizeProto(p string) string {
	if p == "" {
		return "tcp"
	}
	return strings.ToLower(p)
}

func indexOfForward(list []PortForwardView, guestPort int64, proto string) int {
	for i, p := range list {
		if p.GuestPort == guestPort && normalizeProto(p.Proto) == normalizeProto(proto) {
			return i
		}
	}
	return -1
}

func indexOfForward2(list []PortForward, guestPort int64, proto string) int {
	for i, p := range list {
		if p.GuestPort == guestPort && normalizeProto(p.Proto) == normalizeProto(proto) {
			return i
		}
	}
	return -1
}

// sameMounts reports whether a computed list already matches what Lima has,
// so a no-op change does not stop and restart the instance.
func sameMounts(want []Mount, have []MountView) bool {
	if len(want) != len(have) {
		return false
	}
	for i := range want {
		if !samePath(want[i].Location, have[i].Location) {
			return false
		}
		if want[i].MountPoint != "" && !samePath(want[i].MountPoint, have[i].MountPoint) {
			return false
		}
		if want[i].Writable != have[i].Writable {
			return false
		}
	}
	return true
}

// samePortForwards is sameMounts for port forwards.
func samePortForwards(want []PortForward, have []PortForwardView) bool {
	if len(want) != len(have) {
		return false
	}
	for i := range want {
		if want[i].GuestPort != have[i].GuestPort {
			return false
		}
		if want[i].HostPort != 0 && want[i].HostPort != have[i].HostPort {
			return false
		}
		wantProto, haveProto := want[i].Proto, have[i].Proto
		if wantProto == "" {
			wantProto = "tcp"
		}
		if haveProto == "" {
			haveProto = "tcp"
		}
		if !strings.EqualFold(wantProto, haveProto) {
			return false
		}
	}
	return true
}

// TemplateMatch is the outcome of checking a claimed template against an
// instance.
type TemplateMatch int

const (
	// TemplateUnknown means the check could not be performed — the template
	// would not resolve, or one side reported no images.
	TemplateUnknown TemplateMatch = iota
	// TemplateConsistent means the images match. This is *not* proof: several
	// templates share a base image (docker is ubuntu plus provisioning), so a
	// match only rules out an obviously wrong claim.
	TemplateConsistent
	// TemplateMismatch means the images have nothing in common, so the
	// instance definitely did not come from this template.
	TemplateMismatch
)

// VerifyTemplate checks whether an instance plausibly came from a template.
//
// Lima records no template reference on an instance, so this compares the
// resolved disk images — the only stable signal available. `limactl template
// copy --fill` expands the claimed template without creating anything.
//
// The result is deliberately asymmetric, because the evidence is:
//
//   - no shared image  -> TemplateMismatch, a definite refutation
//   - a shared image   -> TemplateConsistent, not a confirmation
//
// Templates that share a base image cannot be told apart this way, which is
// why a match is never reported as proof.
func (s *Service) VerifyTemplate(ctx context.Context, ref string, inst Instance) (TemplateMatch, error) {
	instanceImages := inst.Config.ImageLocations()
	if len(instanceImages) == 0 {
		return TemplateUnknown, nil
	}

	tmpl, err := s.client.ResolveTemplate(ctx, ref)
	if err != nil {
		// A template that will not resolve is not evidence about the
		// instance; the caller decides whether to surface the failure.
		return TemplateUnknown, err
	}

	templateImages := tmpl.ImageLocations()
	if len(templateImages) == 0 {
		return TemplateUnknown, nil
	}

	have := make(map[string]struct{}, len(instanceImages))
	for _, loc := range instanceImages {
		have[loc] = struct{}{}
	}
	for _, loc := range templateImages {
		if _, ok := have[loc]; ok {
			return TemplateConsistent, nil
		}
	}
	return TemplateMismatch, nil
}

// sameDisks reports whether the configured attachment list already matches.
//
// Order matters to Lima, so this is a positional comparison rather than a set
// comparison.
func sameDisks(want []AdditionalDisk, have []AdditionalDiskView) bool {
	if len(want) != len(have) {
		return false
	}
	for i := range want {
		if want[i].Name != have[i].Name {
			return false
		}
	}
	return true
}
