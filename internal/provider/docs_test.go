package provider

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// The docs are hand-written, because they record why the provider behaves as it
// does and no generator derives that from a schema. The cost is that they drift
// silently, and the existing coverage test did not catch it: it asserts only
// that a page exists per type, while the mutability test compares the schema
// against a map hardcoded in the test rather than against the documentation.
//
// Both defects these tests were meant to prevent were live: `timeouts` was
// documented as a block when the schema makes it an attribute, so the documented
// form failed with "Blocks of type timeouts are not expected here", and
// `additional_disks` was absent from the resource page entirely.
//
// These tests derive everything from the schema and parse the real table.

func instanceSchema(t *testing.T) fwresource.SchemaResponse {
	t.Helper()
	resp := fwresource.SchemaResponse{}
	NewInstanceResource().Schema(context.Background(), fwresource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", resp.Diagnostics)
	}
	return resp
}

func instanceDoc(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "resources", "instance.md"))
	if err != nil {
		t.Fatalf("reading the instance docs: %v", err)
	}
	return string(body)
}

func TestEveryInstanceAttributeIsDocumented(t *testing.T) {
	t.Parallel()

	schema := instanceSchema(t)
	doc := instanceDoc(t)

	for name := range schema.Schema.Attributes {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("attribute %q is in the schema but never mentioned in docs/resources/instance.md", name)
		}
	}
	for name := range schema.Schema.Blocks {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("block %q is in the schema but never mentioned in docs/resources/instance.md", name)
		}
	}
}

// mutabilityRows parses the table under "## Update versus replacement", which is
// the one that tells a reader whether a change destroys their VM.
func mutabilityRows(t *testing.T, doc string) map[string]string {
	t.Helper()

	start := strings.Index(doc, "## Update versus replacement")
	if start < 0 {
		t.Fatal("docs/resources/instance.md has no \"Update versus replacement\" section")
	}
	section := doc[start:]
	if end := strings.Index(section[1:], "\n## "); end >= 0 {
		section = section[:end+1]
	}

	row := regexp.MustCompile(`(?m)^\|\s*` + "`" + `([a-z_]+)` + "`" + `\s*\|([^|]*)\|`)
	rows := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		rows[m[1]] = strings.TrimSpace(m[2])
	}
	if len(rows) == 0 {
		t.Fatal("parsed no rows from the mutability table; has its format changed?")
	}
	return rows
}

func TestMutabilityTableMatchesTheSchema(t *testing.T) {
	t.Parallel()

	schema := instanceSchema(t)
	rows := mutabilityRows(t, instanceDoc(t))

	// timeouts is Terraform plumbing rather than a property of the instance, so
	// it has no place in a table about what changing a setting does to a VM.
	skip := map[string]bool{"timeouts": true}

	for name, attr := range schema.Schema.Attributes {
		if skip[name] || (!attr.IsOptional() && !attr.IsRequired()) {
			continue
		}
		behaviour, documented := rows[name]
		if !documented {
			t.Errorf("configurable attribute %q has no row in the mutability table", name)
			continue
		}
		// "Replace" alone means the VM is rebuilt; anything describing an
		// in-place edit does not, even when it also mentions replacement.
		wantReplace := strings.Contains(behaviour, "Replace") && !strings.Contains(behaviour, "In place")
		if got := hasRequiresReplace(attr); got != wantReplace {
			t.Errorf("attribute %q: schema RequiresReplace = %v, but the table says %q",
				name, got, behaviour)
		}
	}

	for name := range rows {
		if _, ok := schema.Schema.Attributes[name]; ok {
			continue
		}
		if _, ok := schema.Schema.Blocks[name]; ok {
			continue
		}
		t.Errorf("the mutability table documents %q, which is not in the schema", name)
	}
}

// The documented status vocabulary must be the one the provider can return.
//
// Both pages once listed `starting` and `stopping`, which NormalizeStatus never
// produces, so a reader could reasonably wait for a transition that never
// arrives.
//
// The check now has two halves, because the schema became the single source of
// truth: statusVocabulary derives the sentence from lima.AllStatuses, and the
// documentation renders it. So the first half asserts the vocabulary itself is
// right, and the second asserts the rendered page really carries it — which is
// what would fail if somebody hand-edited docs/ instead of the template.
func TestDocumentedStatusesMatchTheVocabulary(t *testing.T) {
	t.Parallel()

	vocabulary := statusVocabulary()

	for _, s := range lima.AllStatuses {
		if !strings.Contains(vocabulary, "`"+string(s)+"`") {
			t.Errorf("status %q is in lima.AllStatuses but not in statusVocabulary(): %q", s, vocabulary)
		}
	}
	for _, bogus := range []string{"starting", "stopping"} {
		if strings.Contains(vocabulary, "`"+bogus+"`") {
			t.Errorf("statusVocabulary() offers %q, which the provider never returns: %q", bogus, vocabulary)
		}
	}

	for _, page := range []string{
		filepath.Join("..", "..", "docs", "resources", "instance.md"),
		filepath.Join("..", "..", "docs", "data-sources", "instance.md"),
	} {
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		if !strings.Contains(string(body), vocabulary) {
			t.Errorf("%s does not contain the derived status vocabulary %q; "+
				"regenerate with `make docs` rather than editing docs/ by hand", page, vocabulary)
		}
	}
}

// Documentation under docs/ is generated by tfplugindocs from templates/, and
// hand-editing it is the mistake that guarantee invites: the edit survives until
// the next `make docs` silently reverts it.
//
// Every page carries a generator marker, so its presence is a cheap check that
// nobody replaced a generated page with a hand-written one — and its absence is
// exactly what a well-meaning "I'll just fix this sentence" produces.
func TestEveryDocumentationPageIsGenerated(t *testing.T) {
	t.Parallel()

	const marker = "<!-- schema generated by tfplugindocs -->"

	root := filepath.Join("..", "..")
	pages := []string{filepath.Join(root, "docs", "index.md")}
	for _, dir := range []string{"resources", "data-sources"} {
		entries, err := os.ReadDir(filepath.Join(root, "docs", dir))
		if err != nil {
			t.Fatalf("reading docs/%s: %v", dir, err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".md") {
				pages = append(pages, filepath.Join(root, "docs", dir, entry.Name()))
			}
		}
	}

	for _, page := range pages {
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		if !strings.Contains(string(body), marker) {
			t.Errorf("%s has no tfplugindocs marker, so it is not generated. "+
				"Edit the matching file under templates/ and run `make docs`.", page)
		}
	}
}

// Every registered type needs a template, or `make docs` silently produces a
// bare generated page with none of the reasoning that makes these docs useful.
func TestEveryTypeHasADocumentationTemplate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := New("test")()
	root := filepath.Join("..", "..")

	check := func(kind, typeName string) {
		t.Helper()
		tmpl := filepath.Join(root, "templates", kind, strings.TrimPrefix(typeName, "lima_")+".md.tmpl")
		if _, err := os.Stat(tmpl); err != nil {
			t.Errorf("%s %q has no template at %s", kind, typeName, tmpl)
		}
	}

	for _, fn := range p.Resources(ctx) {
		resp := &fwresource.MetadataResponse{}
		fn().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "lima"}, resp)
		check("resources", resp.TypeName)
	}
	for _, fn := range p.DataSources(ctx) {
		resp := &fwdatasource.MetadataResponse{}
		fn().Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "lima"}, resp)
		check("data-sources", resp.TypeName)
	}
}

// The import warning tells the user what declaring each list attribute will do
// after an import. It claimed all three forced replacement, which stopped being
// true when mounts and port forwards became in-place edits, so it discouraged
// users from a shipped feature.
//
// Deriving the expectation from the plan modifiers means the sentence cannot
// drift from the behaviour again: adding or removing a RequiresReplace without
// rewording the warning fails here.
func TestImportWarningMatchesReplacementBehaviour(t *testing.T) {
	t.Parallel()

	schema := instanceSchema(t)
	detail := importWarningDetail("dev", "project-dev")

	for _, name := range []string{attrMounts, attrPortForwards, attrProvisions} {
		attr, ok := schema.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema has no %q", name)
		}
		claim := paragraphMentioning(detail, name)
		if claim == "" {
			t.Errorf("the import warning never mentions %q", name)
			continue
		}
		saysReplace := strings.Contains(claim, "replace") || strings.Contains(claim, "new instance")
		if want := hasRequiresReplace(attr); saysReplace != want {
			t.Errorf("%s: schema RequiresReplace = %v, but the warning says %q", name, want, claim)
		}
	}
}

// paragraphMentioning returns the paragraph of body that names attr.
//
// The warning is organised one topic per paragraph, which is the unit that
// carries a claim. Splitting on sentences instead would let a claim about one
// attribute be read against another across a paragraph boundary.
func paragraphMentioning(body, attr string) string {
	for _, p := range strings.Split(body, "\n\n") {
		if strings.Contains(p, attr) {
			return strings.TrimSpace(p)
		}
	}
	return ""
}

// docSection returns the body of a top-level documentation section.
func docSection(t *testing.T, doc, heading string) string {
	t.Helper()

	start := strings.Index(doc, heading)
	if start < 0 {
		t.Fatalf("docs/resources/instance.md has no %q section", heading)
	}
	section := doc[start:]
	if end := strings.Index(section[1:], "\n## "); end >= 0 {
		section = section[:end+1]
	}
	return section
}

// The Timeouts section is where a user learns how long an operation may take.
// It documented 30m/20m/20m/2m while the code could only ever produce one
// provider-wide value, because the per-operation fallbacks were unreachable.
// Nothing compared the two, so the discrepancy survived. Parsing the documented
// numbers back out and checking them against the constants they describe is what
// makes that a test failure rather than a surprise during an apply.
func TestDocumentedTimeoutsMatchTheDefaults(t *testing.T) {
	t.Parallel()

	section := docSection(t, instanceDoc(t), "## Timeouts")

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
			t.Errorf("%s: the docs say %s, lima.DefaultTimeouts says %s", m[1], documented, want[m[1]])
		}
		found[m[1]] = true
	}
	for op := range want {
		if !found[op] {
			t.Errorf("the Timeouts section never documents a default for %q", op)
		}
	}
}

// The documented syntax has to be the syntax that works. `timeouts` is an
// attribute — `timeouts = { ... }` — and the block form the docs showed is
// rejected by Terraform before a plan is produced.
func TestTimeoutsIsDocumentedAsAnAttribute(t *testing.T) {
	t.Parallel()

	doc := instanceDoc(t)
	if regexp.MustCompile(`(?m)^\s*timeouts\s*\{`).MatchString(doc) {
		t.Error("docs show `timeouts {` as a block; the schema makes it an attribute, so it must be `timeouts = {`")
	}
	if !strings.Contains(doc, "timeouts = {") {
		t.Error("docs never show the working `timeouts = {` form")
	}
}
