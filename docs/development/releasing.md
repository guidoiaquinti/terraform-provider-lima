# Releasing

How a release actually reaches the Terraform Registry, and the parts that fail
silently if they are skipped.

The mechanics are automated: pushing a `v*` tag runs
[`.github/workflows/release.yml`](../../.github/workflows/release.yml), which
runs GoReleaser over
[`.goreleaser.yml`](../../.goreleaser.yml). Everything below is the part
automation cannot do.

## One-time setup

Each of these is a hard prerequisite. Skipping any of them produces a failure
whose cause is not obvious from the error.

### 1. A GPG signing key

The registry verifies the checksum signature against a public key you give it,
and refuses a release whose signature does not match. Generate an RSA key if you
do not already have one:

```console
$ gpg --full-generate-key        # RSA, 4096 bits, no expiry or a long one
$ gpg --list-secret-keys --keyid-format=long
```

Take the fingerprint from that output. An **expired** key fails verification
later, on a release that otherwise looks fine, so prefer no expiry or set a
reminder.

### 2. Two repository secrets

```console
$ gpg --armor --export-secret-keys <FINGERPRINT> | gh secret set GPG_PRIVATE_KEY
$ gh secret set PASSPHRASE          # the key's passphrase; empty if it has none
```

Confirm both exist — the workflow fails at *Import the GPG signing key* if
either is missing, before any artifact is built:

```console
$ gh secret list
```

`GITHUB_TOKEN` is provided by Actions automatically and needs no setup.

### 3. Connect the repository to the registry

At [registry.terraform.io](https://registry.terraform.io): sign in with GitHub,
then **Publish → Provider**, select this repository, and paste the **public**
half of the signing key:

```console
$ gpg --armor --export <FINGERPRINT>
```

The public key is what makes the signature checkable. Without it the release
publishes on GitHub and then fails to install, which is a confusing place to
discover the problem.

## Per-release

### 1. Decide whether the bump is minor or patch

The provider is pre-1.0, so a breaking schema change is a **minor** bump rather
than a major one — see
[Versioning and stability](../../README.md#versioning-and-stability). The
question to answer is only whether anything in this release forces an existing
configuration or state to change:

- Anything renamed, retyped or removed; a change to what forces replacement; a
  change to an import address → **minor** (`0.1.0` → `0.2.0`).
- Fixes only → **patch** (`0.1.0` → `0.1.1`).

A patch that turns out to have been breaking cannot be taken back: Terraform
will already have selected it for everyone pinned at `~> 0.1.0`. When it is
genuinely unclear, bump the minor.

### 2. Update the files that name a version

- `CHANGELOG.md` — retitle `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`, open
  a fresh empty `## [Unreleased]` above it, and add the two link definitions at
  the foot of the file. Every breaking change needs an entry under `Changed` or
  `Removed` **with its migration steps**; that is the standing trade for being
  allowed to break the schema pre-1.0.
- `main.go` and `Makefile` — the `0.X.Y-dev` default is the version being
  developed *towards*, so bump both to the next version after tagging (step 6).
- `README.md` and `templates/index.md.tmpl` — the `Provider version` column in
  the Lima compatibility table, and the `~> 0.1.0` pin in the Quick start, if
  the minor changed. Run `make docs` afterwards; `make docs-check` enforces it.

### 3. Verify the tree

```console
$ make check
$ make testacc
```

### 4. Tag and push

The tag drives the version. GoReleaser strips the leading `v` for
`main.version`, so tag `v0.1.0` ships a provider reporting `0.1.0`.

```console
$ git tag v0.1.0
$ git push origin v0.1.0
```

`prerelease: auto` means a tag like `v0.1.0-rc1` is marked a prerelease
automatically. Terraform will not select a prerelease for an unconstrained
version requirement, which is useful for a trial run that must not become the
version everyone installs.

### 5. Review and publish the draft

`draft: true` in `.goreleaser.yml` means the workflow creates a **draft**
release, so the generated changelog can be read before anything is public.

**The registry cannot see a draft.** Its webhook fires when a release is
*published*, so a draft left unpublished looks like a release that the registry
simply ignored. Publishing it is a manual step, not an oversight to work around.

Before publishing, check the assets are all present:

```console
$ gh release view v0.1.0 --json assets -q '.assets[].name'
```

Expect the per-platform zips plus these three, all required:

| Asset | Why it matters |
| ----- | -------------- |
| `terraform-provider-lima_0.1.0_SHA256SUMS` | what the registry checksums against |
| `terraform-provider-lima_0.1.0_SHA256SUMS.sig` | verified against your public key |
| `terraform-provider-lima_0.1.0_manifest.json` | declares wire protocol 6 |

The manifest is the easiest of the three to lose, because nothing in a build or
test run needs it — it is published from `terraform-registry-manifest.json` by
the `extra_files` block. `TestRegistryManifestDeclaresProtocolSix` and
`TestGoreleaserPublishesTheRegistryManifest` guard the file and the publishing
rule respectively; if the registry reports the provider as protocol 5, or every
`terraform init` fails against a release that installed fine locally, this is
where to look.

Then publish:

```console
$ gh release edit v0.1.0 --draft=false
```

### 6. Confirm it installed

The registry takes a few minutes to ingest. Then, from an empty directory:

```hcl
terraform {
  required_providers {
    lima = {
      source  = "guidoiaquinti/lima"
      version = "0.1.0"
    }
  }
}
```

```console
$ terraform init
```

A successful `init` here exercises the whole chain — checksum, signature,
protocol version — which no local test can.

### 7. Open the next version

Bump the `-dev` default in `main.go` and `VERSION` in the `Makefile` to the next
version — after `v0.1.0`, both become `0.2.0-dev`. They name the version being
developed towards, so leaving them at the version just released makes every
local build claim to be that release.

Nothing enforces this, because nothing can: a build from an untagged tree has no
correct version to check against. It is a step in this list precisely because it
is the one with no gate behind it.

## Provider address

The address appears in three places and they must agree, or `make install` puts
the binary somewhere Terraform does not look:

| Location | Value |
| -------- | ----- |
| `main.go` | `registry.terraform.io/guidoiaquinti/lima` |
| `Makefile` | `HOSTNAME` / `NAMESPACE` / `NAME` |
| `README.md`, `examples/` | `source = "guidoiaquinti/lima"` |

The namespace comes from the repository owner and the provider name from the
repository name minus its `terraform-provider-` prefix, so
`github.com/guidoiaquinti/terraform-provider-lima` yields
`guidoiaquinti/lima`. `TestProviderAddressAgreesWithTheMakefile` checks the
first two against each other.
