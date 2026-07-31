# This directory is generated

Do not edit anything under `docs/` by hand. The registry-facing pages are
produced by `tfplugindocs` from two sources:

- `templates/` — the hand-written narrative, one `.md.tmpl` per page.
- the provider schema in `internal/provider/` — every attribute reference.

```console
$ make docs          # regenerate docs/ from templates/ and the schema
$ make docs-check    # fail if docs/ is stale or was hand-edited
```

`docs-check` runs in CI on every pull request, so a hand edit here does not
survive review — it stops the build. Change `templates/` or the schema instead,
then run `make docs` and commit the result.

## What the Terraform Registry publishes

The registry only ingests `docs/index.md` and the pages under `docs/guides/`,
`docs/resources/`, and `docs/data-sources/`. Everything else in this directory —
this file, and `docs/development/` — is repository-only and never rendered on
the registry.
