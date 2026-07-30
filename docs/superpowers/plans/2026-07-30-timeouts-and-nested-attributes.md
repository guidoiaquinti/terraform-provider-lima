# Timeouts, nested attributes and import warning — implementation plan

> Executed inline in the authoring session. Commits are deliberately omitted:
> the user asked for the work to stay uncommitted on `main`.

**Goal:** Fix the unreachable per-operation timeout defaults, convert the three
`lima_instance` block types to plural nested attributes, and correct the import
warning that claims mounts and port forwards force replacement.

**Architecture:** Three independent changes against
`docs/superpowers/specs/2026-07-30-timeouts-and-nested-attributes-design.md`.
Tasks 1 and 2 are small and self-contained and land first so the tree stays
green. Task 3 is a schema break whose schema, model, call-site, test and
documentation edits must land together, because `docs_test.go` compares the
documented mutability table against the live schema.

**Tech Stack:** Go 1.26.5, terraform-plugin-framework v1.19.0,
terraform-plugin-framework-timeouts v0.7.0.

## Global Constraints

- No commits. No new branch. Work in place on `main`.
- Do not disturb the 9 files already staged in the index.
- `go build ./...`, `go vet ./...`, `go test ./...` and `golangci-lint run` must
  pass at the end of every task.
- Comments explain *why*, matching the density of the surrounding code.
- Diagnostic text stays in the existing register: full sentences, a concrete
  `limactl` command where one helps.

---

### Task 1: Per-operation timeout defaults

**Files:**
- Modify: `internal/provider/provider.go` — `DefaultTimeout` const, `Configure`,
  `providerData`, new `timeout` method
- Modify: `internal/provider/instance_resource.go:534,602,658,751` — call sites;
  delete `defaultTimeout` at `:921-926`
- Modify: `internal/provider/disk_resource.go:144,192,227,269` — call sites
- Modify: `docs/index.md`, `docs/resources/instance.md`,
  `docs/resources/disk.md` — timeout prose
- Test: `internal/provider/provider_test.go`, `internal/provider/docs_test.go`

**Interfaces:**
- Produces: `func (d *providerData) timeout(perOperation time.Duration) time.Duration`,
  nil-receiver tolerant. Returns `d.DefaultTimeout` when it is `> 0`, else
  `perOperation`.
- Consumes: `lima.DefaultTimeouts` (Create 30m, Update 20m, Delete 20m, Read 2m).

- [ ] **Step 1: Write the failing tests**

In `provider_test.go`:

```go
// The provider-wide default_timeout is an override, not a floor. When it is
// unset every operation must get its own documented default, and a read must
// not silently inherit the half-hour budget a create needs.
func TestProviderDataTimeoutPrefersPerOperationDefault(t *testing.T) {
	t.Parallel()

	unset := &providerData{}
	if got := unset.timeout(lima.DefaultTimeouts.Read); got != lima.DefaultTimeouts.Read {
		t.Errorf("unset default_timeout: read timeout = %s, want %s", got, lima.DefaultTimeouts.Read)
	}
	if got := unset.timeout(lima.DefaultTimeouts.Create); got != lima.DefaultTimeouts.Create {
		t.Errorf("unset default_timeout: create timeout = %s, want %s", got, lima.DefaultTimeouts.Create)
	}

	configured := &providerData{DefaultTimeout: 90 * time.Minute}
	for _, per := range []time.Duration{
		lima.DefaultTimeouts.Create, lima.DefaultTimeouts.Update,
		lima.DefaultTimeouts.Delete, lima.DefaultTimeouts.Read,
	} {
		if got := configured.timeout(per); got != 90*time.Minute {
			t.Errorf("configured default_timeout: timeout(%s) = %s, want 1h30m", per, got)
		}
	}

	// Resources call this before Configure has run.
	var nilData *providerData
	if got := nilData.timeout(lima.DefaultTimeouts.Read); got != lima.DefaultTimeouts.Read {
		t.Errorf("nil providerData: timeout = %s, want %s", got, lima.DefaultTimeouts.Read)
	}
}
```

In `docs_test.go`, close the drift gap that let this bug live:

```go
// The Timeouts section is the only place a user learns how long an operation is
// allowed to take. It documented 30m/20m/20m/2m while the code could only ever
// produce a single provider-wide value, so the numbers are parsed back out and
// compared against the constants they describe.
func TestDocumentedTimeoutsMatchTheDefaults(t *testing.T) {
	t.Parallel()

	doc := instanceDoc(t)
	start := strings.Index(doc, "## Timeouts")
	if start < 0 {
		t.Fatal("docs/resources/instance.md has no Timeouts section")
	}
	section := doc[start:]
	if end := strings.Index(section[1:], "\n## "); end >= 0 {
		section = section[:end+1]
	}

	want := map[string]time.Duration{
		"create": lima.DefaultTimeouts.Create,
		"update": lima.DefaultTimeouts.Update,
		"delete": lima.DefaultTimeouts.Delete,
		"read":   lima.DefaultTimeouts.Read,
	}

	row := regexp.MustCompile("(?m)^- `(create|update|delete|read)` — default `([0-9a-z]+)`")
	found := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		documented, err := time.ParseDuration(m[2])
		if err != nil {
			t.Errorf("%s: documented default %q is not a Go duration: %v", m[1], m[2], err)
			continue
		}
		if documented != want[m[1]] {
			t.Errorf("%s: docs say %s, lima.DefaultTimeouts says %s", m[1], documented, want[m[1]])
		}
		found[m[1]] = true
	}
	for op := range want {
		if !found[op] {
			t.Errorf("the Timeouts section never documents a default for %q", op)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

`go test ./internal/provider/ -run 'TestProviderDataTimeout|TestDocumentedTimeouts' -v`
Expected: FAIL — `unset.timeout` undefined.

- [ ] **Step 3: Implement**

`provider.go`: delete the `DefaultTimeout` const. In `Configure` replace
`timeout := DefaultTimeout` with `var timeout time.Duration` so zero means
unset. Add the method next to `providerData`. Rewrite the `default_timeout`
description to name the per-operation defaults.

`instance_resource.go`: delete `defaultTimeout`; the four call sites become
`r.data.timeout(lima.DefaultTimeouts.Create)` and so on.

`disk_resource.go`: all four call sites become `r.data.timeout(lima.DefaultTimeouts.X)`.

- [ ] **Step 4: Verify**

`go test ./... && go vet ./... && golangci-lint run`
Expected: PASS. `TestDocumentedTimeoutsMatchTheDefaults` passes without doc
edits, because instance.md already documents 30m/20m/20m/2m — it is the code
that was wrong.

- [ ] **Step 5: Documentation**

Update the `default_timeout` prose in `docs/index.md` and the Timeouts sections
of `docs/resources/instance.md` and `docs/resources/disk.md` to state that an
unset value means per-operation defaults and a set one overrides all four. Add a
CHANGELOG "Fixed" entry.

---

### Task 2: Import warning

**Files:**
- Modify: `internal/provider/instance_resource.go:874-885` — the warning
- Modify: `examples/resources/lima_instance/mounts/main.tf:38` — stale comment
- Test: `internal/provider/docs_test.go`

**Interfaces:**
- Consumes: `hasRequiresReplace` from `provider_test.go` (extended in Task 3).
- Produces: `importWarningDetail(logical, actual string) string`, extracted from
  `ImportState` so a test can assert on it without a live import.

- [ ] **Step 1: Write the failing test**

```go
// The import warning tells the user what adding a block after import will do.
// It claimed all three lists force replacement, which stopped being true when
// mounts and port forwards became in-place edits. Deriving the claim from the
// schema means the text cannot drift from the plan modifiers again.
func TestImportWarningMatchesReplacementBehaviour(t *testing.T) {
	t.Parallel()

	schema := instanceSchema(t)
	detail := importWarningDetail("dev", "project-dev")

	for _, name := range []string{attrMounts, attrPortForwards, attrProvisions} {
		attr, ok := schema.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema has no %q", name)
		}
		if !strings.Contains(detail, name) {
			t.Errorf("the import warning never mentions %q", name)
			continue
		}
		replaces := hasRequiresReplace(attr)
		// The sentence naming this attribute is the one that has to be right.
		sentence := sentenceMentioning(detail, name)
		saysReplace := strings.Contains(sentence, "replacement") || strings.Contains(sentence, "replace")
		if saysReplace != replaces {
			t.Errorf("%s: schema RequiresReplace = %v but the warning says %q",
				name, replaces, sentence)
		}
	}
}

// sentenceMentioning returns the sentence of body that names attr.
func sentenceMentioning(body, attr string) string {
	for _, s := range strings.Split(body, ".") {
		if strings.Contains(s, attr) {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
```

- [ ] **Step 2: Run to verify failure**

`go test ./internal/provider/ -run TestImportWarningMatchesReplacementBehaviour -v`
Expected: FAIL — `importWarningDetail` undefined. (This test depends on the Task 3
attribute names, so it is written now and lands green with Task 3; run it after
Task 3 Step 4.)

- [ ] **Step 3: Implement**

Extract the warning body into `importWarningDetail` and reword: mounts and port
forwards are left unset and are applied **in place** on the next apply; only
provisioning forces replacement. Fix the example comment to match.

- [ ] **Step 4: Verify**

`go test ./... && golangci-lint run` — after Task 3.

---

### Task 3: Blocks become plural nested attributes

**Files:**
- Modify: `internal/provider/instance_resource.go` — constants `:32-39`,
  `Schema` `:63-339`, `ValidateConfig` `:342-362`,
  `validateInstanceConfig*` `:364-525`, `Create`/`Read`/`Update` call sites
- Modify: `internal/provider/models.go` — delete `instanceConfigModel`,
  `validationModel`, `hasUnknownBlocks`; add `declaredLists` and
  `instanceModel.declared`; rework `toRenderRequest`,
  `reconcileDeclaredBlocks`, `configuredMounts`, `plannedMounts`,
  `configuredPortForwards`, `plannedPortForwards`
- Modify: `internal/provider/provider_test.go:480-504` — teach
  `hasRequiresReplace` about `ListNestedAttribute` and `ListAttribute`
- Modify: `internal/provider/instance_dynamic_block_test.go` — reframe
- Modify: `internal/provider/instance_resource_test.go`,
  `internal/provider/acceptance_test.go` — renamed fields and HCL
- Modify: `docs/resources/instance.md` — attribute docs and mutability table
- Modify: `examples/resources/lima_instance/{mounts,port_forwards,provisioning}/main.tf`

**Interfaces:**
- Produces:
  - `attrMounts = "mounts"`, `attrPortForwards = "port_forwards"`,
    `attrProvisions = "provisions"`
  - `type declaredLists struct { Mounts []mountModel; PortForwards []portModel;
    Provisions []provModel; MountsManaged, PortForwardsManaged, Unknown bool }`
  - `func (m *instanceModel) declared(ctx context.Context) (declaredLists, diag.Diagnostics)`
  - `func validateInstanceConfig(config *instanceModel, lists declaredLists, namePrefix, home string) diag.Diagnostics`
  - `func (m *instanceModel) toRenderRequest(ctx context.Context, lists declaredLists) (lima.RenderRequest, diag.Diagnostics)`
  - `func reconcileDeclaredBlocks(ctx context.Context, m *instanceModel, lists declaredLists, inst lima.Instance) diag.Diagnostics`
  - `func plannedMounts(lists declaredLists) *[]lima.Mount`,
    `func configuredMounts(lists declaredLists) []lima.Mount`, and the port
    forward equivalents
- Consumes: `mountModel`, `portModel`, `provModel` unchanged.

- [ ] **Step 1: Extend `hasRequiresReplace` first**

It returns `false` in its `default` case, so a `ListNestedAttribute` reports "no
replace" and `TestMutabilityTableMatchesTheSchema` would pass `provisions`
against a "Replace" row only by accident. Add both list cases before touching
the schema:

```go
	case schema.ListNestedAttribute:
		for _, m := range a.PlanModifiers {
			modifiers = append(modifiers, m)
		}
	case schema.ListAttribute:
		for _, m := range a.PlanModifiers {
			modifiers = append(modifiers, m)
		}
```

- [ ] **Step 2: Write the failing tests**

Reframe `instance_dynamic_block_test.go`. `dynamic` blocks no longer apply, so
the hazard becomes an unknown list arriving from a variable:

```go
// A list attribute can still be wholly unknown — `mounts = var.mounts` where the
// variable resolves at apply time. A []mountModel cannot represent unknown, so
// the model holds types.List and decodes only where the value is known.
func TestConfigDecodesUnknownListAttributes(t *testing.T) {
	t.Parallel()

	for _, attr := range []string{attrProvisions, attrMounts, attrPortForwards} {
		t.Run(attr, func(t *testing.T) {
			t.Parallel()
			ctx, cfg := instanceSchemaForTest(t)

			cfg.Raw = configValue(t, ctx, cfg, map[string]tftypes.Value{
				"name":     tftypes.NewValue(tftypes.String, "dev"),
				"template": tftypes.NewValue(tftypes.String, "template:ubuntu"),
				attr:       tftypes.NewValue(blockType(t, ctx, cfg, attr), tftypes.UnknownValue),
			})

			var config instanceModel
			if diags := cfg.Get(ctx, &config); diags.HasError() {
				t.Fatalf("decoding an unknown %s failed: %v", attr, diags)
			}
			lists, diags := config.declared(ctx)
			if diags.HasError() {
				t.Fatalf("declared() diagnostics: %v", diags)
			}
			if !lists.Unknown {
				t.Errorf("declared().Unknown = false, want true for an unknown %s", attr)
			}
		})
	}
}
```

Keep `TestValidateConfigWithUnknownBlocksStillChecksOtherAttributes` but decode
into `instanceModel` and call
`validateInstanceConfig(&config, lists, "", "")`. Delete
`TestInstanceModelsCoverTheSchema` — there is only one model to keep in step
now, and `docs_test.go` already asserts every schema attribute is documented.
Rewrite `TestPlainModeWarnsAboutIgnoredSettings` to build `declaredLists`
directly rather than setting slice fields on the model.

Add the null-versus-empty case, which is the behaviour most at risk in this
refactor:

```go
// An absent mounts attribute means "leave Lima's own mounts alone"; an empty one
// means the user deleted every entry and wants them unmounted. Collapsing the
// two would silently stop honouring a removal.
func TestPlannedMountsDistinguishesAbsentFromEmpty(t *testing.T) {
	t.Parallel()

	if got := plannedMounts(declaredLists{}); got != nil {
		t.Errorf("absent mounts: plannedMounts = %v, want nil", got)
	}
	got := plannedMounts(declaredLists{MountsManaged: true})
	if got == nil {
		t.Fatal("empty mounts: plannedMounts = nil, want a pointer to an empty slice")
	}
	if len(*got) != 0 {
		t.Errorf("empty mounts: plannedMounts = %v, want empty", *got)
	}
}
```

- [ ] **Step 3: Run to verify failure**

`go test ./internal/provider/ 2>&1 | head -30`
Expected: compile failure — `attrMounts`, `declared`, `declaredLists` undefined.

- [ ] **Step 4: Implement the schema**

Rename the constants to `attrMounts`/`attrPortForwards`/`attrProvisions` with
plural values. Delete the `Blocks` map. Add three `schema.ListNestedAttribute`
entries carrying the existing nested attributes, validators and defaults
verbatim; `provisions` keeps `listplanmodifier.RequiresReplace()`. Nested
defaults stay `Optional: true, Computed: true` with their `booldefault` /
`stringdefault`, which is what makes `writable`, `protocol` and `mode` behave as
they do today.

- [ ] **Step 5: Implement the model**

`instanceModel.Mounts`, `PortForwards` and `Provisions` become `types.List` with
the plural `tfsdk` tags. Delete `instanceConfigModel`, `validationModel` and
`hasUnknownBlocks`. Add `declaredLists` and `declared`, which decodes each list
when it is known and non-null, sets the two `Managed` flags from
`!IsNull() && !IsUnknown()`, and sets `Unknown` if any of the three is unknown.

`reconcileDeclaredBlocks` gains a `ctx` and returns diagnostics, rebuilding the
lists with `types.ListValueFrom(ctx, m.Mounts.ElementType(ctx), kept)`.

- [ ] **Step 6: Rewire the call sites**

`ValidateConfig` decodes `instanceModel`, calls `declared`, then
`validateInstanceConfig(&config, lists, prefix, home)`.
`Create`, `Read` and `Update` call `declared` once and thread `lists` into
`toRenderRequest`, `reconcileDeclaredBlocks`, `plannedMounts`,
`configuredMounts`, `plannedPortForwards` and `configuredPortForwards`.
`render` takes `lists` too.

- [ ] **Step 7: Verify**

`go build ./... && go test ./... && go vet ./... && golangci-lint run`
Expected: PASS, including Task 2's `TestImportWarningMatchesReplacementBehaviour`.

- [ ] **Step 8: Documentation, examples and acceptance configs**

Rename the three mutability table rows to plural, convert the "Optional —
blocks" section into attribute documentation showing the `= [{ ... }]` form, and
add a `for`-expression example replacing the `dynamic` guidance. Convert the 5
HCL configurations in `acceptance_test.go` and the 3 example files. Add a
CHANGELOG "Changed" entry recording the break and the migration.

- [ ] **Step 9: Terraform-level verification**

```bash
terraform fmt -check -recursive ./examples
make build
```

Then `terraform validate` one rewritten example through a `dev_overrides`
`~/.terraformrc`, confirming the new syntax is accepted and the old block form
is rejected.

---

## Self-review

**Spec coverage.** Spec §1 → Task 1. Spec §2 schema, model, write-back and blast
radius → Task 3 Steps 4–8. Spec §3 → Task 2. Spec "Verification" → Task 1 Step 4,
Task 3 Steps 7 and 9. Spec "Out of scope" needs no task.

**Placeholders.** None: every code step carries real code, every command has an
expected outcome.

**Type consistency.** `declaredLists` field names are identical in Task 3
Steps 2, 5 and 6 and in the Interfaces block. `attrMounts`/`attrPortForwards`/
`attrProvisions` are used consistently from Task 2 Step 1 onward, which is why
Task 2's test is verified after Task 3. `hasRequiresReplace` is extended in
Task 3 Step 1, before anything depends on its new cases.

**Ordering risk.** Task 2's test references Task 3's constants, so Task 2 Step 4
is deferred to after Task 3 Step 7. This is recorded in both tasks rather than
left implicit.
