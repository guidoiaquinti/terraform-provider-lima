// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// The provider always hands `limactl create` exactly one YAML document. That
// document is assembled here, deterministically, from four layers:
//
//	Lima defaults          (resolved by Lima itself)
//	  base: [template]  OR  raw config YAML
//	  typed Terraform attributes
//	  config_overrides
//
// Lima's own default template composes itself the same way (see
// `limactl info | jq .defaultTemplate.base`), so `base:` is the supported
// composition mechanism rather than a trick.
//
// goccy/go-yaml is used because it is the same library Lima itself parses with,
// which minimises the chance of a document the provider considers valid being
// rejected by Lima.

// BaseRef is one entry of Lima's `base:` list.
type BaseRef struct {
	URL string `yaml:"url"`
}

// Mount is a host directory shared into the guest.
type Mount struct {
	Location   string `yaml:"location"`
	MountPoint string `yaml:"mountPoint,omitempty"`
	Writable   bool   `yaml:"writable"`
}

// PortForward maps a guest port onto the host.
type PortForward struct {
	GuestIP   string `yaml:"guestIP,omitempty"`
	GuestPort int64  `yaml:"guestPort"`
	HostIP    string `yaml:"hostIP,omitempty"`
	HostPort  int64  `yaml:"hostPort,omitempty"`
	Proto     string `yaml:"proto,omitempty"`
}

// AdditionalDisk attaches a Lima disk to an instance.
type AdditionalDisk struct {
	Name string `yaml:"name"`
}

// Provision is one native Lima provisioning step.
type Provision struct {
	Mode   string `yaml:"mode"`
	Script string `yaml:"script"`
}

// InstanceConfig is the typed subset of LimaYAML the provider generates.
//
// Field names and YAML keys were taken from the resolved `config` object that
// Lima emits in `limactl list --format json`, so they are Lima's own spelling
// rather than a guess.
type InstanceConfig struct {
	Base            []BaseRef        `yaml:"base,omitempty"`
	VMType          string           `yaml:"vmType,omitempty"`
	Arch            string           `yaml:"arch,omitempty"`
	CPUs            int64            `yaml:"cpus,omitempty"`
	Memory          string           `yaml:"memory,omitempty"`
	Disk            string           `yaml:"disk,omitempty"`
	Mounts          []Mount          `yaml:"mounts,omitempty"`
	PortForwards    []PortForward    `yaml:"portForwards,omitempty"`
	Provision       []Provision      `yaml:"provision,omitempty"`
	AdditionalDisks []AdditionalDisk `yaml:"additionalDisks,omitempty"`
}

// Clone returns a deep copy so callers can mutate without aliasing slices.
func (c InstanceConfig) Clone() InstanceConfig {
	out := c
	out.Base = append([]BaseRef(nil), c.Base...)
	out.Mounts = append([]Mount(nil), c.Mounts...)
	out.PortForwards = append([]PortForward(nil), c.PortForwards...)
	out.Provision = append([]Provision(nil), c.Provision...)
	out.AdditionalDisks = append([]AdditionalDisk(nil), c.AdditionalDisks...)
	return out
}

// RenderRequest describes one document render.
type RenderRequest struct {
	// Template is a Lima template reference such as "template:ubuntu", or a
	// path to a local template file. Mutually exclusive with RawConfig.
	Template string
	// RawConfig is a complete Lima YAML document supplied by the user.
	// Mutually exclusive with Template.
	RawConfig string
	// Typed holds the typed Terraform attributes, applied over the template
	// or raw config.
	Typed InstanceConfig
	// Overrides is a YAML fragment merged last as an escape hatch.
	Overrides string
}

// Render produces the final Lima YAML document.
//
// Ordering within the document is fixed by the struct field order, and map
// keys from raw config and overrides are sorted, so the same inputs always
// produce byte-identical output. That determinism is what makes config_hash
// meaningful.
func Render(req RenderRequest) ([]byte, error) {
	if req.Template != "" && req.RawConfig != "" {
		return nil, errors.New("template and config are mutually exclusive")
	}

	// Start from the base document: either a `base:` reference to a template,
	// or the user's own YAML parsed into a generic tree.
	var doc map[string]any

	switch {
	case req.RawConfig != "":
		parsed, err := parseMapping(req.RawConfig, "config")
		if err != nil {
			return nil, err
		}
		doc = parsed
	default:
		doc = map[string]any{}
		if req.Template != "" {
			doc["base"] = []any{
				map[string]any{"url": req.Template},
			}
		}
	}

	// Typed attributes are converted through YAML so they land in the tree in
	// exactly the shape Lima expects, then merged over the base.
	typedTree, err := toTree(req.Typed)
	if err != nil {
		return nil, fmt.Errorf("encoding typed attributes: %w", err)
	}
	doc = mergeMaps(doc, typedTree)

	if strings.TrimSpace(req.Overrides) != "" {
		overrideTree, err := parseMapping(req.Overrides, "config_overrides")
		if err != nil {
			return nil, err
		}
		doc = mergeMaps(doc, markEmptySequencesAsClears(overrideTree))
	}

	if !configuresAnything(doc) {
		return nil, errors.New("rendered an empty Lima configuration: set template, config, or at least one VM attribute")
	}

	out, err := marshalDeterministic(doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sanitizeYAMLError strips the source excerpt from a YAML parse error.
//
// goccy/go-yaml renders errors as a message line followed by an annotated
// excerpt of the offending document:
//
//	[1:11] mapping value is not allowed in this context
//	>  1 | password: hunter2
//	         ^
//
// The excerpt would put raw configuration — which may hold credentials or
// provisioning scripts — into a Terraform diagnostic. Only the first line is
// kept: it carries the line/column and the reason, and no content.
func sanitizeYAMLError(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}

// parseMapping decodes a YAML document that must be a mapping.
func parseMapping(src, field string) (map[string]any, error) {
	var v any
	if err := yaml.Unmarshal([]byte(src), &v); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %s", field, sanitizeYAMLError(err))
	}
	if v == nil {
		return map[string]any{}, nil
	}
	m, ok := normalizeValue(v).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a YAML mapping, got %T", field, v)
	}
	return m, nil
}

// toTree round-trips a typed value through YAML into a generic tree so that
// omitempty is honoured and the result merges like any other document.
func toTree(v any) (map[string]any, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	m, ok := normalizeValue(out).(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return m, nil
}

// normalizeValue converts any map[any]any produced by the YAML decoder into
// map[string]any, recursively, so merging and marshalling have one shape to
// handle. Scalars are returned untouched: no int is coerced to string and no
// string to bool.
func normalizeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeValue(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeValue(val)
		}
		return out
	default:
		return v
	}
}

// mergeMaps returns a new tree with src merged over dst.
//
// Mappings merge key by key so an override can touch one nested field without
// discarding its siblings. Sequences are replaced wholesale rather than
// concatenated: appending would make `mounts` grow every time the provider
// re-rendered, and there is no sensible identity by which to match elements.
// An explicit null removes the key, which is the only way to unset something a
// template set.
//
// A removal is written out as `key: null` rather than by dropping the key,
// which is what makes it work against a template. A template is rendered as a
// `base:` reference, so Lima merges the template's keys in *after* this
// document is complete; a dropped key is then indistinguishable from one that
// was never set, and the template's value wins. Lima honours an explicit null
// as "clear this" across that merge, so that is what gets emitted. Verified
// against Lima 2.2.0; see docs/development/lima-cli-contract.md §12.11 and
// issue #12.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = deepCopy(v)
	}
	for k, v := range src {
		if v == nil {
			out[k] = nil
			continue
		}
		existing, ok := out[k]
		if !ok {
			out[k] = deepCopy(v)
			continue
		}
		em, eok := existing.(map[string]any)
		sm, sok := v.(map[string]any)
		if eok && sok {
			out[k] = mergeMaps(em, sm)
			continue
		}
		out[k] = deepCopy(v)
	}
	return out
}

// markEmptySequencesAsClears rewrites every empty sequence in an overlay layer
// into an explicit null.
//
// The two spellings mean the same thing here — "replace this sequence with
// nothing" — but Lima's `base:` merge only honours one of them: an empty
// sequence in the child document is treated as unset and the template's own
// entries survive, while a null clears them. Rewriting at the boundary is what
// keeps `mounts: []` and `mounts: null` from behaving differently.
//
// Only overlay layers are rewritten. A raw `config` is the user's complete
// document rather than a fragment merged over something, so it is passed to
// Lima exactly as written. Sequence *elements* are left alone for the same
// reason: a sequence replaces its counterpart wholesale, so nothing inside one
// is merged with anything and an empty list in there is just an empty list.
func markEmptySequencesAsClears(tree map[string]any) map[string]any {
	out := make(map[string]any, len(tree))
	for k, v := range tree {
		switch t := v.(type) {
		case []any:
			if len(t) == 0 {
				out[k] = nil
				continue
			}
			out[k] = v
		case map[string]any:
			out[k] = markEmptySequencesAsClears(t)
		default:
			out[k] = v
		}
	}
	return out
}

// configuresAnything reports whether a rendered document sets anything at all.
//
// Keys whose value is a clear do not count: a document that only removes keys
// asks Lima for nothing, and the caller's diagnostic ("set template, config, or
// at least one VM attribute") is the useful answer rather than whatever Lima
// would say about a configuration with no images.
func configuresAnything(doc map[string]any) bool {
	for _, v := range doc {
		if v != nil {
			return true
		}
	}
	return false
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = deepCopy(val)
		}
		return out
	default:
		return v
	}
}

// marshalDeterministic serialises a tree with sorted keys at every level.
//
// Go map iteration is randomised, so sorting is what makes two renders of the
// same configuration byte-identical — and therefore what makes config_hash
// stable across plans.
func marshalDeterministic(doc map[string]any) ([]byte, error) {
	ordered := toOrdered(doc)
	b, err := yaml.MarshalWithOptions(ordered,
		yaml.Indent(2),
		// Aliases and anchors would make output depend on shared pointers
		// rather than on content. Everything is deep-copied above, so no
		// shared references remain to alias.
		yaml.UseLiteralStyleIfMultiline(true),
	)
	if err != nil {
		return nil, fmt.Errorf("serialising Lima configuration: %w", err)
	}
	return b, nil
}

// toOrdered converts maps into yaml.MapSlice with sorted keys, recursively.
func toOrdered(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		ms := make(yaml.MapSlice, 0, len(keys))
		for _, k := range keys {
			ms = append(ms, yaml.MapItem{Key: k, Value: toOrdered(t[k])})
		}
		return ms
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = toOrdered(val)
		}
		return out
	default:
		return v
	}
}

// HashDocument returns the config hash recorded in state.
//
// It hashes the rendered bytes, which are already normalised and
// deterministically ordered, so insignificant whitespace or key-order changes
// in the user's raw YAML do not move the hash.
func HashDocument(doc []byte) string {
	sum := sha256.Sum256(doc)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidateYAML reports whether s parses as a YAML mapping, for plan-time
// checks that must not shell out.
func ValidateYAML(s, field string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	_, err := parseMapping(s, field)
	return err
}
