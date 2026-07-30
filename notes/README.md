# Notes

Working documents kept for provenance: the plan and design spec for a change,
written before it was implemented.

They are **not** documentation, they are not maintained after the work lands,
and they may describe things that were subsequently decided against. For what
the provider actually does, read [`../docs/`](../docs/); for why a decision was
made, read [`../CHANGELOG.md`](../CHANGELOG.md) and
[`../ROADMAP.md`](../ROADMAP.md), both of which are kept current.

These files previously lived under `docs/`. They were moved out because `docs/`
is the directory the Terraform Registry scans and publishes, so it should hold
only what a user of the provider is meant to read.
