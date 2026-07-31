## What this changes

<!-- A short description. Link any related issue. -->

## Checklist

- [ ] `make check` passes
- [ ] Tests were added or updated alongside the behaviour change
- [ ] `docs/development/lima-cli-contract.md` is updated if command behaviour
      changed, and was verified against a real `limactl`
- [ ] The mutability tables in `README.md` and
      `templates/resources/instance.md.tmpl` match the plan modifiers in the code
- [ ] `docs/` was regenerated with `make docs` if any schema description or
      template changed — and was **not** edited by hand
- [ ] `CHANGELOG.md` is updated
- [ ] Acceptance tests were run (`make testacc`) if anything touching Lima
      changed — or state below why not

## Acceptance test results

<!-- Paste the summary, or explain why they were not run. -->

## AI assistance

<!--
See CONTRIBUTING.md § Use of AI. Disclosure is required, not discouraged — the
project is itself AI-assisted. Name the tool, and flag anything you have not
verified yourself.
-->

- [ ] No AI tool was used for this change
- [ ] An AI tool was used — tool(s): <!-- e.g. Claude Code, Copilot -->

If a tool was used, confirm:

- [ ] Every claim about `limactl` behaviour was verified by running it, not
      generated from memory
- [ ] Every new test was watched failing before the change that makes it pass
- [ ] Anything I have *not* personally verified is called out below

<!-- Unverified areas, if any: -->
