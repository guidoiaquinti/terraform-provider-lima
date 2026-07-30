---
page_title: "lima_instance Resource - terraform-provider-lima"
subcategory: ""
description: |-
  Manages a local Lima virtual machine instance through limactl.
---

# lima_instance (Resource)

Manages a local Lima virtual machine.

The provider composes a **single** Lima YAML document from the chosen template
or raw configuration, the typed attributes below, and `config_overrides`, then
hands it to `limactl create`. It never edits files inside `LIMA_HOME`.

## Example usage

```hcl
resource "lima_instance" "dev" {
  name     = "project-dev"
  template = "template:ubuntu"

  cpus   = 4
  memory = "8GiB"
  disk   = "50GiB"

  mounts = [
    {
      location    = abspath(path.module)
      mount_point = "/workspace"
      writable    = true
    },
  ]

  port_forwards = [
    {
      guest_port = 8080
      host_port  = 18080
      protocol   = "tcp"
    },
  ]

  provisions = [
    {
      mode        = "system"
      script      = file("${path.module}/bootstrap.sh")
      rerun_token = filesha256("${path.module}/bootstrap.sh")
    },
  ]
}
```

## Configuration precedence

Later layers win:

```text
Lima defaults
  → template (rendered as a base: reference) or raw config
    → typed attributes (vm_type, arch, cpus, memory, disk, mounts, port_forwards, provisions)
      → config_overrides
```

Merge semantics inside that chain:

- **Mappings merge key by key.** An override can change one nested field
  without discarding its siblings.
- **Sequences are replaced wholesale.** Appending would make lists such as
  `mounts` grow on every render, and there is no reliable identity by which to
  match elements.
- **An explicit `null` removes a key.** This is the only way to unset something
  a template set.

Generation is deterministic: keys are sorted at every level and no YAML anchors
or aliases are emitted, so the same inputs always produce byte-identical output.
That is what makes `config_hash` meaningful.

## Schema

### Required

- `name` (String) Logical instance name. The real Lima name is this value
  prefixed with the provider's `name_prefix`, exposed as `instance_name`.
  Must start with a letter or digit and contain only letters, digits, dots,
  dashes and underscores, up to 63 characters.

### Optional — instance source

Set exactly one of these. Setting neither produces a warning and relies
entirely on Lima's defaults.

- `template` (String) Lima template to build on, such as `template:ubuntu`,
  `template:docker`, or a path to a local template file. Rendered as a `base:`
  entry. Conflicts with `config`.
- `config` (String, Sensitive) Complete Lima YAML used instead of `template`.
  Normalised before hashing, so whitespace and key-order changes do not
  produce a diff. Conflicts with `template`.
- `config_overrides` (String, Sensitive) YAML fragment merged last, as an
  escape hatch for Lima options without a typed attribute.

### Optional — typed VM attributes

- `vm_type` (String) Backend, for example `vz` or `qemu`. Unrecognised values
  produce a **warning**, not an error, so a newer Lima backend works without a
  provider upgrade.
- `arch` (String) Architecture, for example `aarch64` or `x86_64`.
- `cpus` (Number) Virtual CPU count. Must be at least 1. Applied **in place**.
- `memory` (String) Memory size, for example `4GiB` or `8192MiB`. Normalised,
  so equivalent spellings do not differ. Applied **in place**.
- `disk` (String) Primary disk size. Normalised. Growth is applied **in
  place**; **shrinking is rejected at plan time.**
- `start` (Boolean) Whether the instance should be running after apply.
  Defaults to `true`.
- `protect` (Boolean) Whether Lima's deletion protection is enabled. Defaults
  to `false`.
- `additional_disks` (List of String) Names of `lima_disk` disks to attach, in
  order. Each is mounted in the guest at the disk's `mount_point`, normally
  `/mnt/lima-<name>`. Applied **in place**, stopping and restarting a running
  instance. A disk is locked while the instance holding it runs, so detach it
  here before destroying or resizing the `lima_disk`.
- `timeouts` (Attribute) See [Timeouts](#timeouts).

### Optional — nested lists

These three are **attributes**, not blocks, so each takes an equals sign and a
list of objects:

```hcl
mounts = [
  { location = abspath(path.module), mount_point = "/workspace", writable = true },
]
```

Because they are ordinary list attributes, entries can be derived with a `for`
expression rather than a `dynamic` block:

```hcl
mounts = [for d in var.shared_dirs : { location = d, writable = true }]
```

Order is preserved in all three; Lima treats it as significant.

#### `mounts`

- `location` (String, **Required**) Absolute host path. A leading `~` is
  expanded. Symlinks are **not** resolved, so the value stays stable across
  plans.
- `mount_point` (String) Guest path. Defaults to Lima's behaviour of reusing
  `location`.
- `writable` (Boolean) Defaults to `false`.

Lima rejects guest **system** paths such as `/etc` or `/usr` as mount points.
On macOS, mounting `/tmp` without an explicit `mount_point` fails for this
reason, because `/tmp` resolves to `/private/tmp` and is treated as a system
path. Set `mount_point` explicitly in that case.

Host portability: mount locations are absolute host paths, so a configuration
with `/Users/alice/project` will not apply on a Linux host. Use
`abspath(path.module)` or a variable to keep configurations portable.

Setting `mounts = []` is meaningfully different from omitting it. An omitted
attribute leaves Lima's own mounts alone; an empty list is a request to unmount
everything the provider previously declared.

#### `port_forwards`

- `guest_port` (Number, **Required**) 1–65535.
- `host_port` (Number) 1–65535. Lima chooses one when omitted.
- `protocol` (String) `tcp` or `udp`. Defaults to `tcp`.
- `guest_ip` (String) Guest-side bind address.
- `host_ip` (String) Host-side bind address, for example `0.0.0.0` to expose
  the forward beyond loopback.

Duplicate guest ports and duplicate host ports are rejected at plan time.

The provider does **not** expose guest IP addresses. They are not reliable
across Lima's networking modes; use the forwarded host endpoint instead.

As with `mounts`, an empty list and an omitted attribute differ.

#### `provisions`

- `mode` (String) Lima provisioning mode. Defaults to `system`.
- `script` (String, **Required**, Sensitive) Script body. Never echoed in
  diagnostics or logs.
- `rerun_token` (String) Arbitrary value whose change forces replacement,
  typically `filesha256(...)`.

Provisioning runs **during instance creation**, which is Lima's own model. The
provider does not use Terraform's `remote-exec` and never re-runs scripts on
refresh or apply. To re-run provisioning, change `rerun_token` (or any other
provisioning field), which replaces the instance.

### Read-only

- `id` (String) The real Lima instance name; also the import ID.
- `instance_name` (String) The real Lima instance name (`name_prefix` + `name`).
- `status` (String) Normalised status: `running`, `stopped`, `starting`,
  `stopping`, `creating`, `broken` or `unknown`.
- `raw_status` (String) The status exactly as Lima reported it, e.g. `Running`.
- `ssh_address` (String) Host address for SSH, normally `127.0.0.1`.
- `ssh_port` (Number) Host port forwarded to guest SSH.
- `ssh_user` (String) Guest login name.
- `ssh_config` (String) Path to Lima's generated SSH configuration file.
- `hostname` (String) Guest hostname, normally `lima-<instance_name>`.
- `dir` (String) Instance directory inside `LIMA_HOME`.
- `config_hash` (String) SHA-256 of the effective generated configuration.
- `lima_version` (String) Lima version recorded against the instance.

Connect with:

```hcl
output "ssh_command" {
  value = "ssh -F ${lima_instance.dev.ssh_config} ${lima_instance.dev.hostname}"
}
```

**On a stopped instance, `ssh_address` and `ssh_port` are last-known values,
not a live endpoint.** Lima keeps reporting them after a stop; the provider
passes them through rather than pretending otherwise.

No private key material is placed in state. `ssh_config` is a path.

## Timeouts

- `create` — default `30m`
- `update` — default `20m`
- `delete` — default `20m`
- `read` — default `2m`

Setting the provider's `default_timeout` replaces **all four**, so a value chosen
to accommodate a slow creation also applies to every refresh. Leave it unset to
keep the per-operation defaults above.

`timeouts` is an **attribute**, not a block, so it takes an equals sign:

```hcl
resource "lima_instance" "dev" {
  # ...
  timeouts = {
    create = "45m"
  }
}
```

Writing it as a block fails before planning, with
`Blocks of type "timeouts" are not expected here`.

Timeouts are enforced through context cancellation, so a `limactl` process is
signalled rather than left running.

## Update versus replacement

This table is generated from the plan modifiers in the schema and reflects
actual behaviour.

| Attribute          | Update behaviour                                         |
| ------------------ | -------------------------------------------------------- |
| `name`             | Replace                                                  |
| `template`         | Replace                                                  |
| `config`           | Replace                                                  |
| `config_overrides` | Replace                                                  |
| `vm_type`          | Replace                                                  |
| `arch`             | Replace                                                  |
| `cpus`             | **In place** — stop, `limactl edit`, restart             |
| `memory`           | **In place** — stop, `limactl edit`, restart             |
| `disk`             | **In place** growth; shrinking **fails at plan time**    |
| `start`            | In place — start or stop                                 |
| `protect`          | In place — `limactl protect` / `unprotect`               |
| `mounts`           | **In place** — stop, `limactl edit --set`, restart       |
| `port_forwards`    | **In place** — stop, `limactl edit --set`, restart       |
| `provisions`       | Replace                                                  |
| `additional_disks` | **In place** — stop, `limactl edit --set`, restart       |

### In-place resizing

`cpus`, `memory` and `disk` are applied with `limactl edit`, which opens no
editor when explicit flags are supplied:

```console
$ limactl edit --tty=false --cpus 4 --memory 2 project-dev
level=info msg="Instance `project-dev` configuration edited"
```

Lima refuses to edit a **running** instance
(`cannot edit a running instance`), so the provider performs:

1. inspect, and record whether the instance is running
2. stop it if it is
3. apply the edit
4. start it again, but only if the desired state calls for it

**This means a running instance is restarted.** Expect brief downtime on a
resize. The disk and everything on it survive; only the VM process is cycled.
If the same apply also sets `start = false`, the instance is simply left down
rather than being started just to be stopped again.

#### Size spellings are preserved

Sizes are compared as **byte counts**, so `8GiB` and `8192MiB` mean the same
thing and no resize is performed when you switch between them.

The provider also keeps whatever spelling you wrote. A refresh does not rewrite
`8192MiB` to `8GiB` in state, because Terraform decides whether an update is
needed by comparing your raw configuration to prior state — rewriting it would
make every plan show a change whose apply did nothing.

Changing only the spelling therefore updates the recorded string once and
leaves the VM completely untouched; no `limactl edit`, stop or start runs.

#### If the restart fails

The provider distinguishes this case explicitly, because the configuration
change *did* take effect:

```text
Lima instance "project-dev" was reconfigured but could not be restarted

The configuration change was applied successfully, so the instance now has the
requested resources — it is simply stopped.

Run `terraform apply` again to retry the start, or investigate with:

    limactl list project-dev
    limactl start --debug project-dev
```

If the **edit itself** fails, the instance is deliberately left stopped rather
than restarted with its old configuration, so a running VM cannot be mistaken
for a successful change.

A worked example lives in
[`examples/resources/lima_instance/resizing`](https://github.com/guidoiaquinti/terraform-provider-lima/tree/main/examples/resources/lima_instance/resizing).

#### Removing an attribute does not resize

Deleting `cpus` from your configuration leaves the instance's CPU count where
it is. Lima has no "revert to the template default" operation for an existing
instance, and inferring what the default would have been could silently shrink
the VM.

### Why the remaining attributes still replace

`arch` has no `edit` flag, and changing it would invalidate the disk image.
`mounts` has `--mount` flags, but they append rather than declaratively replace
a list, so reconciling a Terraform list against them is not reliable.
`port_forwards` and `provisions` have no `edit` flags at all. `vm_type` does have
`--vm-type`, but switching backend under an existing disk image is not a change
the provider can verify is safe, so it stays a replacement.

Where a change cannot be applied *and verified*, replacement is the honest
behaviour.

### Disk shrinking

Shrinking is rejected during planning rather than silently triggering a
replacement, because a replacement would destroy the disk's contents. To make
a disk smaller, destroy and recreate the instance deliberately.

## Drift detection

The provider **detects**:

- an instance deleted outside Terraform — it is removed from state
- running versus stopped, reflected in `status` and `start`
- protection state, reflected in `protect`
- CPU, memory, disk, VM type and architecture drift, but **only for attributes
  you set explicitly**

- **mount and port-forward divergence**, as an ordinary diff (see below)

The provider **does not** detect:

- provisioning drift — Lima does not report which scripts ran
- changes made by editing `lima.yaml` directly that Lima does not report back

### Mount and port-forward drift is restored in place

Every `mounts` and `port_forwards` entry you declare is compared against Lima's
resolved configuration on refresh. If one was changed or removed outside
Terraform, state is updated to match reality, so `terraform plan` shows an
ordinary diff and `terraform apply` **restores it in place** — the instance is
stopped, reconfigured and started again, not recreated.

Only **declared** entries are compared. Lima's resolved lists also contain
entries the base template contributed (the default templates mount your home
directory) and defaults Lima filled in such as `guestIP`. Those are never
written into state, so they cannot cause a plan to propose removing blocks you
never wrote.

False positives are avoided by:

- comparing paths after cleaning, tolerating macOS `/tmp` → `/private/tmp`;
- keeping an unset `mount_point` unset while Lima is only reporting its
  default, which is the location itself;
- keeping an unset `host_port` unset, since Lima chose that value.

An earlier release only emitted a warning here, because the sole remedy then
was destroying the VM. Now that mounts and port forwards are reconcilable, a
diff is both safer and more useful.

## Protection behaviour

Setting `protect = true` runs `limactl protect`, so Lima refuses `limactl
delete`.

While protected, `terraform destroy` **fails** with an error explaining how to
proceed. The provider deliberately does **not** unprotect automatically — doing
so would make the flag meaningless as a safeguard.

To destroy a protected instance, either set `protect = false` and apply first,
or run `limactl unprotect <name>` yourself.

A `true → false` transition is applied in place at the start of the update, so
a protected instance can be reconfigured and unprotected in a single apply.

## Import

The import ID is always the **real** Lima instance name, including any
`name_prefix`:

```console
$ terraform import lima_instance.example project-dev
```

With `name_prefix = "acme-"`, importing `acme-dev` yields `name = "dev"`, which
re-derives to `acme-dev`. Importing a name that does not start with the prefix
keeps it whole.

### What import populates

Everything Lima actually reports is recorded, so a configuration matching
reality plans clean:

`cpus`, `memory`, `disk`, `vm_type`, `arch`, `start`, `protect`, and all the
read-only attributes.

**Left unset:** `template`, `config`, `config_overrides`, `mounts`,
`port_forwards` and `provisions`. Lima does not record which template an
instance came from, and its resolved configuration cannot be distinguished
from defaults, so populating these would invent configuration.

### Adoption: declaring what already exists

`template`, `config`, `config_overrides`, `vm_type` and `arch` force
replacement only on a **real change between two known values**. Two
transitions are deliberately exempt:

| Transition | Behaviour | Why |
| ---------- | --------- | --- |
| `null` → value | No replacement | An imported instance has no recorded template. Declaring one describes what already exists; rebuilding the VM would destroy data to end up where it started. |
| value → `null` | No replacement | Removing an attribute means "stop managing this", not "rebuild the machine". |
| value → different value | **Replaces** | A genuine change that cannot be applied to a live instance. |

So the import workflow is:

```console
$ terraform import lima_instance.dev project-dev
$ terraform plan     # shows what to write
```

Write the reported values plus the template you believe it came from, then
apply. The first apply reconciles `template` and `config_hash` into state as an
**in-place update that does not touch the VM**; after that, plans are clean.

Because `cpus`, `memory` and `disk` are applied in place and `Resize` compares
against what Lima actually reports, declaring resources that already match
performs no edit, stop or start at all.

### Declaring the wrong template is detected

If you declare a `template` the instance did not come from, the provider says
so:

```text
Warning: Declared template does not match the instance

Instance "project-dev" uses none of the disk images that "template:ubuntu"
resolves to, so it was almost certainly not created from that template.

The declaration has been recorded anyway, because Lima does not track which
template an instance came from and the value only takes effect if the instance
is later replaced.
```

It compares the instance's resolved disk images against those of the declared
template, obtained with `limactl template copy --fill` — no instance is
created and nothing is downloaded.

**This refutes, it does not confirm.** `template:docker` is `template:ubuntu`
plus provisioning, so the two resolve to the same image and cannot be told
apart. A mismatch means the claim is definitely wrong; the absence of a
mismatch only means it is plausible.

It is a warning rather than an error because the value has no effect until a
future replacement, and someone adopting an instance may legitimately not know
its exact origin. Blocking the apply would make import harder for no safety
gain.

Adding `mounts` or `port_forwards` entries after import does **not** plan a
replacement. They are applied in place on the next apply, exactly as any other
change to them is: the instance is stopped, reconfigured with
`limactl edit --set` and started again.

Adding `provisions` **does** plan a replacement, because Lima runs provisioning
only at creation time and offers no supported way to re-run it.

## Partial creation

If `limactl create` fails after Lima has already registered the instance, the
provider:

1. leaves the instance in place — it never auto-deletes a VM that may hold data
2. writes it to Terraform state, so `terraform destroy` can clean it up
3. reports in the error that this happened, and gives the manual cleanup command

The same applies when creation succeeds but starting fails.
