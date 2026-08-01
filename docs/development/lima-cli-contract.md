# Lima CLI contract

Everything in this document was **observed directly** against a real Lima
installation. Nothing here is recalled from memory.

```text
limactl version 2.2.0
host: darwin/aarch64 (macOS, Apple Silicon)
go:   1.26.5
```

Commands were exercised inside an isolated `LIMA_HOME` (`/tmp/ltfdisco`) so the
user's real `~/.lima` was never mutated. Only read-only commands were run
against the default home.

This file is the source of truth for `internal/lima`. If a future Lima release
changes any behaviour below, update this document **and** the fixtures in
`internal/testutil/fixtures/` together.

---

## 1. Non-interactive operation

`limactl` opens an editor and prompts when stdout is a TTY. Both are disabled
by a global flag:

```text
--tty        Enable TUI interactions such as opening an editor.
             Defaults to true when stdout is a terminal.
             Set to false for automation.
-y, --yes    Alias of --tty=false
```

The provider passes `--tty=false` to **every** mutating command. Observed
result when creating with `--tty=false`:

```text
level=info msg="Terminal is not available, proceeding without opening an editor"
```

Because the provider captures stdout into a pipe, Lima would already treat the
stream as non-TTY, but the flag is passed explicitly so behaviour does not
depend on how the process was spawned.

## 2. Version

```console
$ limactl --version
limactl version 2.2.0
```

Single line, `limactl version X.Y.Z`. Development builds may carry a suffix
(e.g. `2.2.0-12-gabcdef`), so the parser accepts a leading semver core and
ignores trailing metadata.

## 3. Host information — `limactl info`

Emits a single JSON document on stdout. Relevant top-level keys observed:

| Key             | Example                                    | Used for                      |
| --------------- | ------------------------------------------ | ----------------------------- |
| `version`       | `"2.2.0"`                                  | `lima_host.lima_version`      |
| `limaHome`      | `"/Users/alice/.lima"`                     | `lima_host.lima_home`         |
| `vmTypes`       | `["qemu","vz","krunkit"]`                  | `lima_host.vm_types`          |
| `hostOS`        | `"darwin"`                                 | `lima_host.host_os`           |
| `hostArch`      | `"aarch64"`                                | `lima_host.host_arch`         |
| `templates`     | `[{"name":"docker","location":"..."}]`     | `lima_host.templates`         |
| `defaultTemplate` | full resolved LimaYAML document          | (not exposed; too large)      |
| `identityFile`  | `"/Users/alice/.lima/_config/user"`        | (not exposed; key material)   |

`templates` contained 124 entries on the test host, including internal ones
prefixed with `_` (`_images/…`, `_default/…`). The provider filters those out
of `lima_host.templates` because they are composition fragments, not templates
a user would instantiate.

`limactl info` is cheap (no VM interaction), so the host data source calls it
directly.

## 4. Listing and inspecting — `limactl list`

```text
limactl list [flags] [INSTANCE]...
  -f, --format string   json, yaml, table, go-template  (default "table")
      --all-fields      Show all fields
  -q, --quiet           Only show names
```

### 4.1 Output is NDJSON, not a JSON array

This is the single most important parsing detail. With two instances present:

```console
$ limactl list --format json
{"name":"second","hostname":"lima-second","status":"Stopped",...}
{"name":"tfdisco","hostname":"lima-tfdisco","status":"Stopped",...}
```

Two lines, two independent JSON objects, **no enclosing `[...]`**. A single
instance yields exactly one line. Zero instances yield empty stdout plus a
warning on stderr. The provider therefore decodes with a streaming
`json.Decoder` in a loop rather than unmarshalling into a slice.

### 4.2 `--all-fields` is required

Without it, `protected`, `limaVersion` and `sshAddress` are omitted. Field sets
observed for the same running instance:

| Field            | default | `--all-fields` | Notes                                  |
| ---------------- | :-----: | :------------: | -------------------------------------- |
| `name`           |   yes   |      yes       |                                        |
| `hostname`       |   yes   |      yes       | `lima-<name>`                          |
| `status`         |   yes   |      yes       |                                        |
| `dir`            |   yes   |      yes       | instance directory                     |
| `vmType`         |   yes   |      yes       | `vz`, `qemu`, `krunkit`                |
| `arch`           |   yes   |      yes       | `aarch64`, `x86_64`                    |
| `cpus`           |   yes   |      yes       | integer                                |
| `memory`         |   yes   |      yes       | **bytes as integer**, e.g. `1073741824`|
| `disk`           |   yes   |      yes       | **bytes as integer**                   |
| `sshLocalPort`   | running |    running     | omitted while `0`                      |
| `sshConfigFile`  |   yes   |      yes       | path to generated `ssh.config`         |
| `hostAgentPID`   | running |    running     |                                        |
| `driverPID`      | running |    running     |                                        |
| `sshAddress`     |   no    |      yes       | `127.0.0.1`                            |
| `protected`      |   no    |      yes       | boolean                                |
| `limaVersion`    |   no    |      yes       | version that created the instance      |
| `HostOS`         |   no    |      yes       | note the capitalised key               |
| `HostArch`       |   no    |      yes       | note the capitalised key               |
| `LimaHome`       |   no    |      yes       | note the capitalised key               |
| `IdentityFile`   |   no    |      yes       | **never surfaced into state**          |
| `config`         |   yes   |      yes       | full resolved LimaYAML, see below      |

The capitalised `HostOS` / `HostArch` / `LimaHome` / `IdentityFile` keys are
inconsistent with the rest of the document but are reproduced verbatim by the
parser struct tags.

### 4.3 `memory` and `disk` are bytes at the top level, strings inside `config`

```json
{
  "memory": 1073741824,
  "disk": 8589934592,
  "config": { "memory": "1GiB", "disk": "8GiB" }
}
```

The provider reads the **top-level integers** for drift detection (they are
unambiguous) and formats them back into IEC strings for display.

### 4.4 SSH user comes from `config.user.name`

```json
"user": {"name": "alice", "home": "/home/alice.guest", "shell": "/bin/bash", "uid": 501}
```

Cross-checked against the generated `ssh.config`:

```text
IdentityFile "/private/tmp/ltfdisco/_config/user"
User alice
Hostname 127.0.0.1
Port 61627
```

### 4.5 `sshAddress` / `sshLocalPort` persist after stop

After stopping a previously-running instance, `sshLocalPort` still reported
`61627` and `sshAddress` still `127.0.0.1`. These are **last-known** values, not
proof of a live endpoint. Documented as such in the resource docs.

### 4.6 Not-found is exit 1 with an *empty stdout*

```console
$ limactl list --format json --all-fields nosuch
level=warning msg="No instance matching nosuch found."
level=fatal msg="unmatched instances"
$ echo $?
1
```

The important part is not the message but that **stdout is empty**. Measured
against Lima 2.2.0:

| Argument            | Exit | stdout            |
| ------------------- | ---- | ----------------- |
| an existing name    | 0    | one JSON object   |
| a missing name      | 1    | *nothing*         |
| existing + missing  | 1    | the existing one  |

So a name-scoped lookup identifies absence **structurally** — "the command failed
and produced no object" — without reading Lima's wording. That is what lets
`Inspect` scope its list to the one name it wants, instead of listing every
instance and filtering in Go. The earlier full-list approach was chosen to avoid
text matching, but cost a full `--all-fields` listing per call: a single create
issues five of them, each making Lima resolve the configuration of every instance
in the home.

Two caveats the implementation has to respect:

- **A cancelled command looks identical**: non-zero exit, no output. Cancellation
  is therefore checked first, or a timeout would be reported as a missing
  instance.
- **Name matching is exact.** `limactl list eucloud` does not match
  `eucloud-global-1`, so a scoped list cannot return a different instance that
  merely shares a prefix.

The `unmatched instances` / `No instance matching` markers remain in `IsNotFound`
as a fallback for errors that arrive by another route.

`limactl disk list` takes **no** name argument, so disk lookups still list every
disk and filter in Go. That is a Lima limitation, not a choice.

### 4.7 Status vocabulary

Observed on this host: `Running`, `Stopped`. Lima additionally defines
`Uninitialized`, `Installing`, `Broken` and an empty unknown status. The
provider normalises all six and preserves the original in `raw_status`; any
value it does not recognise maps to `unknown` with the raw value retained, so a
future Lima status cannot break a refresh.

## 5. Creating — `limactl create`

```text
limactl create FILE.yaml|URL [flags]
      --name string   Override the instance name
```

The positional argument is **one** of: a local YAML file, a URL, `template:NAME`,
or `-` for stdin. There is no way to pass a template *and* a config file
simultaneously.

### 5.1 Composition uses `base:`, not flag stacking

Lima's own `defaultTemplate` (from `limactl info`) is built this way:

```json
"base": [{"url": "template:_images/ubuntu"}, {"url": "template:_default/mounts"}]
```

Verified that a hand-written file composes a published template with overrides:

```yaml
base:
  - template:alpine
cpus: 2
memory: 1GiB
disk: 8GiB
mounts:
  - location: /private/tmp/x
    writable: false
portForwards:
  - guestPort: 8080
    hostPort: 18080
    proto: tcp
provision:
  - mode: system
    script: |
      #!/bin/sh
      echo hello
```

```console
$ limactl validate t.yaml
level=info msg="`t.yaml`: OK"
$ limactl create --tty=false --name=tfdisco t.yaml
... level=info msg="Run `limactl start tfdisco` to start the instance."
```

The resulting instance reported `cpus: 2`, `memory: 1GiB`, `disk: 8GiB` — the
overrides won over the base template. **This is the mechanism the provider
uses**: it always generates exactly one YAML document and hands it to
`limactl create` as a temporary file. `--cpus` / `--memory` / `--set` flags are
never used, so there is exactly one code path and one thing to hash.

This maps cleanly onto the required precedence:

```text
Lima defaults  →  base: [template]  or  raw config  →  typed attributes  →  config_overrides
```

### 5.2 `--set` and yq are deliberately unused

`--set` accepts yq expressions but they are restricted (`limactl help
yq-restrictions`) and would require the provider to generate an expression
language. The `base:` approach is strictly more expressive and fully
deterministic.

### 5.3 Duplicate name

```console
$ limactl create --tty=false --name=tfdisco t.yaml
level=fatal msg="instance `tfdisco` already exists"
```

Note the backticks around the name. Lima quotes the object it is talking about
in a genuine collision, and does **not** in its other "already exists" messages
— see §5.4. The provider's marker is anchored on `` ` already exists`` for that
reason.

Detection is structural first: the lifecycle layer checks for the instance under
the instance lock and returns `ErrAlreadyExists`, which the resource renders as
an import hint. The message match is only a fallback.

### 5.4 Concurrent first creates race on the shared keypair

Lima generates the per-home SSH keypair in `<LIMA_HOME>/_config/user` on first
use, by shelling out to `ssh-keygen`, with no locking. Four concurrent creates
into an empty home:

```console
$ for n in raw1 raw2 raw3 raw4; do limactl create --tty=false --name=$n t.yaml & done; wait
$ limactl list --format '{{.Name}}'
raw1
```

One succeeds. The others fail with:

```text
level=fatal msg="failed to run [ssh-keygen -t ed25519 -q -N  -C lima -f <home>/_config/user]:
  \"<home>/_config/user already exists.\nOverwrite (y/n)? \": exit status 1"
```

Two consequences for the provider:

- A per-instance lock cannot prevent this, because the contested resource
  belongs to the home. `Service.awaitSharedSetup` serialises creates until one
  has succeeded, then lets them run concurrently again, so the cost is paid once
  per process rather than on every create.
- The message contains "already exists" but is **not** a name collision. Matching
  it as one made the provider advise `terraform import` for an instance that did
  not exist, which is why the marker is backtick-anchored (§5.3).

Measured against Lima 2.2.0.

### 5.5 Create does not start

Create leaves the instance `Stopped`. `start = false` therefore needs no extra
work beyond not calling start.

### 5.6 Instance name length is bounded by LIMA_HOME path length

```console
$ LIMA_HOME=/very/long/path limactl create --name=tfdisco t.yaml
level=fatal msg="instance name `tfdisco` too long:
  `/very/long/path/tfdisco/ssh.sock.1234567890123456` must be less than
  UNIX_PATH_MAX=104 characters, but is 189"
```

The limit is on `len(LIMA_HOME) + len(name) + len(\"/ssh.sock.\")+16 + 1 < 104`.
The provider validates this at plan time where `home` is known and emits an
actionable error instead of letting create fail mid-apply. Acceptance tests
must use a short `LIMA_HOME` such as `/tmp/ltfacc`.

## 6. Validating — `limactl validate`

```console
$ limactl validate t.yaml       # exit 0
level=info msg="`t.yaml`: OK"

$ limactl validate bad.yaml     # exit 1
level=fatal msg="failed to unmarshal YAML (bad.yaml): [1:7] cannot unmarshal
  string into Go struct field LimaYAML.CPUs of type int
>  1 | cpus: \"notanumber\"
             ^"
```

Real schema validation with line/column diagnostics, no VM interaction. The
provider runs it against the generated document before every create so bad
configuration fails fast with Lima's own error text.

Validation covers more than YAML syntax. It also enforces semantic rules the
provider deliberately does **not** duplicate, for example:

```console
$ limactl validate mounts.yaml
level=fatal msg="failed to validate YAML file `mounts.yaml`:
  field `mounts[0].mountPoint` must not be a system path such as /etc or /usr"
```

That rule was discovered by running the provider's own generated documents
through `limactl validate` in `TestRealLimactlAcceptsRenderedDocuments`. Note
that on macOS a mount of `/tmp` defaults its `mountPoint` to `/tmp`, which
Lima treats as a system path — such a mount needs an explicit `mount_point`.

Re-implementing these checks in the provider would mean maintaining a second,
always-stale copy of Lima's schema. Delegating to `limactl validate` means the
user gets Lima's own wording, and new Lima rules apply with no provider
release.

## 7. Starting — `limactl start`

```text
limactl start NAME|FILE.yaml|URL [flags]
      --timeout duration   Duration to wait for the instance to be running
```

Observed: `limactl start --tty=false tfdisco` blocked for ~55s on an Alpine VM
and exited 0 after printing `READY. Run 'limactl shell tfdisco' ...`.

Start is **synchronous** — it returns only once the instance is running. The
provider still polls status afterwards to confirm, because a synchronous exit
code alone does not tell us the final observable state.

### 7.1 A non-zero exit does not mean the instance is not running

Observed repeatedly on loaded CI runners: `limactl start` brings the VM up,
reports `The final requirement 1 of 1 is satisfied`, then fails to forward the
guest agent socket over SSH and exits **1**:

```text
error: [guest agent does not seem to be running; port forwards will not work]
warning: DEGRADED. The VM seems running, but file sharing and port forwarding
         may not work.
fatal: degraded, status={Running:true Degraded:true Exiting:false ...}
```

Note `Running:true` alongside the non-zero exit. The instance exists, is
running, and is usable — only port forwarding and file sharing are in doubt.

The provider therefore treats the exit code as advisory for `start`: on failure
it inspects the instance, and if Lima reports it running, the start is accepted
and a warning is logged rather than the apply failing. Reporting an error would
tell the user their instance could not be created while it is demonstrably
running. A start that leaves the instance in any other state is still an error.

### 7.2 The wait before giving up is Lima's, and is not configurable

`pkg/hostagent/requirements.go` retries each requirement with function-local
constants `retries = 200` and `sleepDuration = 3 * time.Second` — 600s, with no
environment variable or flag to shorten it. `limactl start --timeout` bounds the
CLI's own wait and defaults to `DefaultWatchHostAgentEventsTimeout = 10 *
time.Minute`; the provider does not pass it, so that default applies. Both
limits are ten minutes, and neither is ours: a VM that never gets to `ssh`
occupies a create for ten minutes before failing.

## 8. Stopping — `limactl stop`

```text
limactl stop INSTANCE [flags]
  -f, --force   Force stop the instance
```

Graceful stop of a running instance:

```console
$ limactl stop --tty=false tfdisco
level=info msg="Waiting for the instance to shut down"
level=info msg="The instance tfdisco has shut down"
```

**Stopping an already-stopped instance is an error:**

```console
$ limactl stop --tty=false tfdisco
level=fatal msg="expected status `Running`, got `Stopped` (maybe use `limactl stop -f`?)"
```

The provider therefore inspects status first and treats "already stopped" as
success without invoking stop. This is why the lifecycle layer is
status-driven rather than command-driven.

## 9. Deleting — `limactl delete`

```text
limactl delete INSTANCE [INSTANCE, ...] [flags]
  -f, --force   Forcibly kill the processes
```

Deleting a stopped instance succeeds and cleans up its directory:

```console
$ limactl delete --tty=false second
level=info msg="Removing *.pid *.sock *.tmp under `/private/tmp/ltfdisco/second`"
level=info msg="Deleted `second` (`/private/tmp/ltfdisco/second`)"
```

**Deleting an absent instance is exit 0** — delete is idempotent:

```console
$ limactl delete --tty=false second
level=warning msg="Ignoring non-existent instance `second`"
$ echo $?
0
```

`-f` also stops a running instance, so the provider passes `--force` on delete
and does not need a separate stop step.

## 10. Protection — `limactl protect` / `unprotect`

```console
$ limactl protect second
level=info msg="Protected `second`"
```

`protected: true` then appears in `list --all-fields` output.

```console
$ limactl delete --tty=false second
level=fatal msg="failed to delete instance `second`: instance is protected to
  prohibit accidental removal (Hint: use `limactl unprotect`)"
```

```console
$ limactl unprotect second
level=info msg="Unprotected `second`"
```

The provider never auto-unprotects on destroy. See the resource docs for the
rationale.

## 11. SSH information — `limactl show-ssh` is NOT used

```console
$ limactl show-ssh --format json tfdisco
level=warning msg="`limactl show-ssh` is deprecated. Instead, use
  `ssh -F /private/tmp/ltfdisco/tfdisco/ssh.config lima-tfdisco`."
level=fatal msg="unknown format: `json`"
```

`show-ssh` is deprecated **and** has no machine-readable format — its only
formats are `cmd`, `args` and `options`, all human-oriented text.

**Deviation from the original design sketch:** the proposed `Client` interface
had a `ShowSSH(ctx, name) (SSHInfo, error)` method. It is not implemented as a
separate command. Every field it would return is already present in
`limactl list --format json --all-fields`:

| SSH field   | Source                    |
| ----------- | ------------------------- |
| address     | `sshAddress`              |
| port        | `sshLocalPort`            |
| user        | `config.user.name`        |
| config file | `sshConfigFile`           |
| hostname    | `hostname`                |

`SSHInfo` is therefore derived from the inspect result in the domain layer, and
no deprecated, unparseable command is invoked. This satisfies the rule "do not
parse human-oriented tables when machine-readable output exists".

## 12. Editing — `limactl edit`

```text
limactl edit INSTANCE|FILE.yaml [flags]
      --cpus int         Number of CPUs
      --memory float32   Memory in GiB
      --disk float32     Disk size in GiB
      --vm-type string   Virtual machine type
      --set stringArray  Modify the template inplace, using yq syntax
      --start            Start the instance after editing
```

> **Correction.** An earlier revision of this document stated that no safe
> non-interactive edit existed, on the assumption that `edit` always needs
> `$EDITOR`. That is wrong: when explicit flags are supplied, no editor is
> opened. The behaviour below was measured directly against Lima 2.2.0, and
> the provider's in-place resize support rests on it.

### 12.1 Explicit flags need no editor

```console
$ limactl edit --tty=false --cpus 4 --memory 2 edt
level=info msg="Instance `edt` configuration edited"
$ echo $?
0
```

`cpus` went 2 → 4 and `memory` 1GiB → 2GiB, confirmed via
`limactl list --format json`. Only the flags supplied are changed; every other
field — `base`, `images`, `mounts`, `portForwards`, `vmType`, `arch` — is
preserved.

### 12.2 The instance must be stopped

```console
$ limactl edit --tty=false --cpus 3 edt     # while Running
level=fatal msg="cannot edit a running instance"
$ echo $?
1
```

The instance was left untouched and still Running. This is why the provider's
resize path is stop → edit → conditional restart rather than a bare edit.

### 12.3 Disk grows but never shrinks

```console
$ limactl edit --tty=false --disk 12 edt    # from 8GiB
level=info msg="Instance `edt` configuration edited"        # exit 0

$ limactl edit --tty=false --disk 4 edt     # from 12GiB
level=fatal msg="the YAML is invalid, saved the buffer as `lima.REJECTED.yaml`:
  field `disk`: shrinking the disk (12GiB --> 4GiB) is not supported"
```

Lima enforces grow-only itself. The provider also rejects shrinking at **plan**
time so the user finds out before an apply starts, but Lima is the backstop.

Note the side effect: a rejected edit writes `lima.REJECTED.yaml` into the
instance directory. The provider does not read or remove it — that file is
Lima's, and it is useful evidence for the user.

### 12.4 `--memory` and `--disk` are float GiB, and are byte-exact in practice

The flags take GiB as a `float32`, not an IEC string, so the provider converts
`8GiB` to `8` and `1000MiB` to `0.9765625`. That conversion is lossless for any
MiB-granular size: `N MiB / 1024` is a dyadic rational, exactly representable
in float32 while `N < 2^24` (16 TiB).

Measured round-trips, comparing the requested size to the byte count Lima
reported back:

| Requested  | Flag value  | Bytes reported | Exact |
| ---------- | ----------- | -------------- | ----- |
| `8GiB`     | `8`         | 8589934592     | yes   |
| `1000MiB`  | `0.9765625` | 1048576000     | yes   |
| `7000MiB`  | `6.8359375` | 7340032000     | yes   |
| `512MiB`   | `0.5`       | 536870912      | yes   |
| `16GiB`    | `16`        | 17179869184    | yes   |

The provider still refuses to build a flag it cannot represent exactly, rather
than silently resizing to a slightly different value.

One cosmetic consequence: Lima records the value as it received it, so
`lima.yaml` shows `memory: 0.9765625GiB` rather than `1000MiB`. This does not
affect the provider, which compares the **top-level byte counts** from
`limactl list`, not the config strings.

### 12.5 `--set` is still not used

`--set` takes yq expressions with documented restrictions
(`limactl help yq-restrictions`). The typed flags cover everything the provider
needs and require no expression generation, so `--set` remains unused.

### 12.6 A resized instance boots normally

```console
$ limactl edit --tty=false --cpus 2 --memory 1 edt
$ limactl start --tty=false edt
level=info msg="READY. Run `limactl shell edt` to open the shell."      # exit 0
```

### 12.7 Mounts and port forwards: use `--set`, not `--mount-only`

`limactl edit` has mount flags, but they are **not** usable for declarative
reconciliation. Measured:

```console
# Instance created from a document declaring one mount.
# Resolved: /private/tmp -> /workspace (mine), /Users/alice (from the template).

$ limactl edit --tty=false --mount-only /private/tmp:w mnt
# Resolved: /private/tmp -> /private/tmp     <- ONLY one mount left
```

Two problems, both disqualifying:

1. `--mount-only` **discards the mounts the base template contributed**. The
   `_default/mounts` fragment in most templates mounts the user's home
   directory; an edit would silently unmount it.
2. Its syntax is `location[:w]`, so it **cannot express a guest mount point**.
   The example above lost `/workspace` and remounted at `/private/tmp`.

There is no `--port-forward` flag on `edit` at all.

`--set` is the usable path. It takes a yq expression and accepts a full JSON
structure:

```console
$ limactl edit --tty=false \
    --set '.mounts = [{"location":"/private/tmp","mountPoint":"/workspace","writable":true}]' mnt
$ limactl edit --tty=false \
    --set '.portForwards = [{"guestPort":9090,"hostPort":19090,"proto":"tcp"}]' mnt
```

Both applied exactly, `mountPoint` included.

**Escaping is safe.** Values are embedded with Go's `json.Marshal`, and a path
containing a double quote and a space round-tripped byte-exact:

```console
$ limactl edit --tty=false --set '.mounts = [{"location":"/tmp/we ird\"path", ...}]' mnt
# Resolved: /tmp/we ird"path -> /mnt/x
```

Lima additionally disables yq's `env`, `load`, `load_str` and `system`
operators (`limactl help yq-restrictions`), so even a malformed expression
cannot read the environment or execute anything.

### 12.8 `--set` operates on the already-merged configuration

This is the subtlety that shapes how the provider reconciles mounts.

On **create**, Lima merges the base template's mounts with the instance's own.
A document declaring two mounts produced three:

```text
[0] /private/tmp     -> /workspace          (declared)
[1] /private/var/tmp -> /scratch            (declared)
[2] /Users/alice     -> /Users/alice        (from template:_default/mounts)
```

Declared entries come first, in order, followed by template-contributed ones.

On **edit**, `--set .mounts = [...]` replaces that *already-merged* list
outright — base merging does not run again. So an in-place mount change must
write:

```text
new declared mounts (in order)  +  the entries the template contributed
```

The provider computes the second group by subtracting the previously
configured mounts (from Terraform state) from the currently resolved list. If a
previously configured mount cannot be located — meaning it was changed outside
Terraform — the provider refuses the in-place edit and directs the user to
`terraform apply -replace=...` rather than guessing.

An acceptance test pins the resulting equivalence: an instance edited to
configuration Y has the same resolved mounts as one freshly created with Y.

### 12.9 Resolving a template without creating anything

`limactl template copy --fill <ref> -` expands a template reference to its
effective YAML on stdout. No instance is created and nothing is downloaded:

```console
$ limactl template copy --fill template:alpine -
images:
- location: https://dl-cdn.alpinelinux.org/alpine/v3.23/.../nocloud_alpine-3.23.4-x86_64-uefi-cloudinit-r0.qcow2
  ...
```

`--fill` applies Lima's defaults, which makes the result comparable to an
instance's resolved `config`. `--embed-all` also exists but inlines external
dependencies, which is slower and changes nothing about the image list.

This is what makes template verification possible. An instance records no
template reference, so the disk images are the only stable signal of where it
came from.

**The check refutes, it does not confirm.** Measured:

| Template          | Resolved image                                     |
| ----------------- | -------------------------------------------------- |
| `template:alpine` | `dl-cdn.alpinelinux.org/.../nocloud_alpine-…qcow2`  |
| `template:ubuntu` | `cloud-images.ubuntu.com/.../ubuntu-26.04-…img`     |
| `template:docker` | `cloud-images.ubuntu.com/.../ubuntu-26.04-…img`     |

`docker` is `ubuntu` plus provisioning, so the two are **indistinguishable by
image**. A mismatch is therefore proof the claim is wrong; a match is only
consistency. The provider reports it that way, and an integration test asserts
both properties against the real catalogue.

### 12.10 What is still not editable

`arch` has no `edit` flag, and changing it would invalidate the disk image
anyway. Mounts have `--mount` flags, but they append rather than declaratively
replace, so reconciling a Terraform list against them is not reliable. Port
forwards and provisioning have no `edit` flags at all.

Those attributes therefore keep `RequiresReplace`. `vm_type` has a
`--vm-type` flag but is left as replace as well: switching backend on an
existing disk image is not a change the provider can verify is safe.

## 13. Disks — `limactl disk`

```text
limactl disk create DISK --size SIZE [--format qcow2]
limactl disk list [--json]
limactl disk resize DISK --size SIZE
limactl disk delete DISK [DISK, ...] [-f]
limactl disk unlock DISK [DISK, ...]
```

### 13.1 `--json` is NDJSON again

```console
$ limactl disk list --json
{"name":"data","size":1073741824,"format":"raw","dir":"/private/tmp/ltfdisk/_disks/data","instance":"","instanceDir":"","mountPoint":"/mnt/lima-data"}
```

One object per line, no enclosing array — the same shape as `limactl list`. An
empty `LIMA_HOME` produces empty stdout and exit 0.

| Field         | Meaning                                                     |
| ------------- | ----------------------------------------------------------- |
| `name`        | disk name                                                     |
| `size`        | **bytes as integer**                                          |
| `format`      | the format Lima detects on disk, see §13.3                    |
| `dir`         | `<LIMA_HOME>/_disks/<name>`                                   |
| `instance`    | instance currently holding the disk, `""` when free           |
| `instanceDir` | that instance's directory, `""` when free                     |
| `mountPoint`  | guest path, defaulting to `/mnt/lima-<name>`                  |

### 13.2 Exit codes

Measured without a pipe, so these are the real statuses:

| Operation                  | Exit | Message                                                     |
| -------------------------- | :--: | ----------------------------------------------------------- |
| create a duplicate         |  1   | ``disk `data` already exists (`…/_disks/data`)``              |
| delete a non-existent disk |  0   | ``Ignoring non-existent disk `nosuch``` — idempotent          |
| resize smaller             |  1   | ``specified size `512MiB` is less than the current disk size `2GiB`. Disk shrinking is currently unavailable`` |
| resize to the same size    |  0   | idempotent                                                    |
| resize larger              |  0   | ``Resized disk `data```                                       |

### 13.3 `format` does not round-trip

```console
$ limactl disk create q2 --size 1GiB --format qcow2
level=info msg="Creating qcow2 disk `q2` with size 1GiB"

$ limactl disk list --json | grep q2
{"name":"q2", … "format":"raw", …}
```

Creating with `--format qcow2` produced a **raw** file (`_disks/q2/datadisk`,
1 GiB sparse, no metadata sidecar), and `list` reports `raw`. The `vz` driver
requires raw images, so Lima converts.

The provider therefore treats `format` as a **create-time request** that is
never read back into state — reading it would fight the configuration on every
plan — and exposes what Lima reports separately as `actual_format`.

### 13.4 A disk is locked only while its instance is running

Attaching happens in the instance's configuration:

```yaml
additionalDisks:
  - name: data
```

With the instance **stopped**, the disk still reports `instance: ""`. Only once
it starts does the lock appear:

```console
$ limactl start --tty=false wd
$ limactl disk list --json | grep data
{"name":"data", … "instance":"wd","instanceDir":"/private/tmp/ltfdisk/wd", …}
```

So `instance` means "currently in use by a running VM", not "attached to".

### 13.5 An in-use disk cannot be deleted or resized

```console
$ limactl disk delete data
level=fatal msg="cannot delete disk `data` in use by instance `wd`"          # exit 1

$ limactl disk resize data --size 4GiB
level=fatal msg="cannot resize disk `data` used by running instance `wd`.
  Please stop the VM instance"                                                # exit 1
```

The provider surfaces both rather than working around them. Stopping someone's
VM to resize a disk would mean one resource silently acting on another, which
is not a decision a provider should make on its own — so `lima_disk` reports
what to do and stops there.

`limactl disk unlock` exists for the case where a force-stopped instance leaves
a stale lock. The provider does **not** call it: it cannot tell a stale lock
from a live one, and unlocking a disk a running VM is writing to risks
corruption.

### 13.6 Attachment can be changed in place

`additionalDisks` responds to the same `--set` mechanism as mounts:

```console
$ limactl edit --tty=false --set '.additionalDisks = [{"name":"q2"},{"name":"rawd"}]' wd
# resolved: [{"name": "q2"}, {"name": "rawd"}]

$ limactl edit --tty=false --set '.additionalDisks = []' wd
# resolved: null
```

Note that setting an empty list resolves to `null` rather than `[]`, so the
provider must treat both as "no additional disks".

## 14. Summary of commands the provider invokes

| Purpose        | Command                                                     |
| -------------- | ----------------------------------------------------------- |
| version        | `limactl --version`                                           |
| host info      | `limactl info`                                                |
| inspect / list | `limactl list --format json --all-fields [NAME]`              |
| validate       | `limactl validate <tmpfile>`                                  |
| create         | `limactl create --tty=false --name=<name> <tmpfile>`          |
| resize         | `limactl edit --tty=false [--cpus N] [--memory G] [--disk G] <name>` |
| remount        | `limactl edit --tty=false --set '.mounts = [...]' <name>`     |
| re-forward     | `limactl edit --tty=false --set '.portForwards = [...]' <name>` |
| start          | `limactl start --tty=false <name>`                            |
| stop           | `limactl stop --tty=false <name>`                             |
| delete         | `limactl delete --tty=false --force <name>`                   |
| resolve template | `limactl template copy --fill <ref> -`                      |
| disk list      | `limactl disk list --json`                                    |
| disk create    | `limactl disk create <name> --size <s> [--format <f>]`        |
| disk resize    | `limactl disk resize <name> --size <s>`                       |
| disk delete    | `limactl disk delete <name>`                                  |
| protect        | `limactl protect <name>`                                      |
| unprotect      | `limactl unprotect <name>`                                    |

No other `limactl` subcommand is used. No file under `LIMA_HOME` is written,
moved or removed by the provider.
