// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import (
	"strings"
	"testing"
)

// Golden documents are written inline rather than in separate files so a
// change to rendering shows up directly in the diff of this test.

func TestRenderTemplateOnly(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{Template: "template:ubuntu"})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	// A template alone becomes a single `base:` reference, which is Lima's
	// own composition mechanism.
	want := "base:\n- url: template:ubuntu\n"
	if string(got) != want {
		t.Errorf("Render() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderTypedOnly(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{Typed: InstanceConfig{
		VMType: "vz",
		Arch:   "aarch64",
		CPUs:   4,
		Memory: "8GiB",
		Disk:   "50GiB",
	}})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	// Keys are sorted, which is what makes the output byte-stable.
	want := strings.Join([]string{
		"arch: aarch64",
		"cpus: 4",
		"disk: 50GiB",
		"memory: 8GiB",
		"vmType: vz",
		"",
	}, "\n")
	if string(got) != want {
		t.Errorf("Render() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderTypedOverridesTemplate(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{
		Template: "template:docker",
		Typed:    InstanceConfig{CPUs: 8, Memory: "16GiB"},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := strings.Join([]string{
		"base:",
		"- url: template:docker",
		"cpus: 8",
		"memory: 16GiB",
		"",
	}, "\n")
	if string(got) != want {
		t.Errorf("Render() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderRawConfigOnly(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{RawConfig: "cpus: 2\nmemory: 2GiB\nvmType: qemu\n"})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "cpus: 2\nmemory: 2GiB\nvmType: qemu\n"
	if string(got) != want {
		t.Errorf("Render() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderTypedOverridesRawConfig(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{
		RawConfig: "cpus: 2\nmemory: 2GiB\nvmType: qemu\n",
		Typed:     InstanceConfig{CPUs: 16},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	// Typed wins over raw config; untouched keys survive.
	if !strings.Contains(string(got), "cpus: 16") {
		t.Errorf("typed cpus did not override raw config:\n%s", got)
	}
	if !strings.Contains(string(got), "memory: 2GiB") {
		t.Errorf("raw config memory was lost:\n%s", got)
	}
	if !strings.Contains(string(got), "vmType: qemu") {
		t.Errorf("raw config vmType was lost:\n%s", got)
	}
}

func TestRenderPrecedenceChain(t *testing.T) {
	t.Parallel()

	// Full documented chain: template -> typed -> config_overrides.
	got, err := Render(RenderRequest{
		Template:  "template:ubuntu",
		Typed:     InstanceConfig{CPUs: 4, Memory: "8GiB"},
		Overrides: "cpus: 12\nnestedVirtualization: true\n",
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	s := string(got)
	if !strings.Contains(s, "cpus: 12") {
		t.Errorf("config_overrides did not win over typed cpus:\n%s", s)
	}
	if !strings.Contains(s, "memory: 8GiB") {
		t.Errorf("typed memory was lost:\n%s", s)
	}
	if !strings.Contains(s, "url: template:ubuntu") {
		t.Errorf("template base was lost:\n%s", s)
	}
	if !strings.Contains(s, "nestedVirtualization: true") {
		t.Errorf("override-only key was lost:\n%s", s)
	}
}

func TestRenderNestedOverrideMerging(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{
		RawConfig: "ssh:\n  localPort: 2222\n  forwardAgent: false\n",
		Overrides: "ssh:\n  forwardAgent: true\n",
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	s := string(got)
	// Merging a nested mapping must not discard sibling keys.
	if !strings.Contains(s, "localPort: 2222") {
		t.Errorf("sibling key was discarded by nested merge:\n%s", s)
	}
	if !strings.Contains(s, "forwardAgent: true") {
		t.Errorf("nested override did not apply:\n%s", s)
	}
}

func TestRenderExplicitNullRemovesKey(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{
		RawConfig: "cpus: 4\nmemory: 8GiB\n",
		Overrides: "memory: null\n",
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if strings.Contains(string(got), "memory") {
		t.Errorf("explicit null did not remove the key:\n%s", got)
	}
	if !strings.Contains(string(got), "cpus: 4") {
		t.Errorf("unrelated key was removed:\n%s", got)
	}
}

func TestRenderSequencesAreReplacedNotAppended(t *testing.T) {
	t.Parallel()

	got, err := Render(RenderRequest{
		RawConfig: "mounts:\n- location: /a\n- location: /b\n",
		Typed: InstanceConfig{
			Mounts: []Mount{{Location: "/c", Writable: true}},
		},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	s := string(got)
	// Appending would make the list grow on every render, so replacement is
	// the only stable choice.
	if strings.Contains(s, "/a") || strings.Contains(s, "/b") {
		t.Errorf("sequence was appended rather than replaced:\n%s", s)
	}
	if !strings.Contains(s, "/c") {
		t.Errorf("replacement sequence missing:\n%s", s)
	}
}

func TestRenderMountOrderIsStable(t *testing.T) {
	t.Parallel()

	cfg := InstanceConfig{Mounts: []Mount{
		{Location: "/z", Writable: true},
		{Location: "/a"},
		{Location: "/m", MountPoint: "/mnt/m"},
	}}

	first, err := Render(RenderRequest{Typed: cfg})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	// Rendering repeatedly must be byte-identical, and the author's order
	// must be preserved rather than sorted.
	for i := range 20 {
		again, err := Render(RenderRequest{Typed: cfg.Clone()})
		if err != nil {
			t.Fatalf("Render returned error: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("render %d differed:\n%s\nvs\n%s", i, again, first)
		}
	}

	zi := strings.Index(string(first), "/z")
	ai := strings.Index(string(first), "/a")
	mi := strings.Index(string(first), "/m")
	if zi >= ai || ai >= mi {
		t.Errorf("mount order was not preserved:\n%s", first)
	}
}

func TestRenderPortForwardOrderIsStable(t *testing.T) {
	t.Parallel()

	cfg := InstanceConfig{PortForwards: []PortForward{
		{GuestPort: 9090, HostPort: 19090, Proto: "tcp"},
		{GuestPort: 80, HostPort: 8080, Proto: "tcp"},
		{GuestPort: 53, HostPort: 5353, Proto: "udp"},
	}}

	first, err := Render(RenderRequest{Typed: cfg})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	for i := range 20 {
		again, err := Render(RenderRequest{Typed: cfg.Clone()})
		if err != nil {
			t.Fatalf("Render returned error: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("render %d differed", i)
		}
	}
	if a, b := strings.Index(string(first), "9090"), strings.Index(string(first), "8080"); a > b {
		t.Errorf("port forward order was not preserved:\n%s", first)
	}
}

func TestRenderProvisionSpecialCharacters(t *testing.T) {
	t.Parallel()

	// A script full of YAML-hostile characters must round-trip byte-exactly.
	script := "#!/bin/bash\nset -eu\necho \"quotes: 'single' \\\"double\\\"\"\n" +
		"echo 'tab\there'\nexport X='a: b'\nprintf '%s\\n' \"#not-a-comment\"\n" +
		"echo \"trailing space \"\n"

	got, err := Render(RenderRequest{
		Template: "template:ubuntu",
		Typed: InstanceConfig{Provision: []Provision{
			{Mode: "system", Script: script},
		}},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	// Parse it back and confirm the script survived intact.
	parsed, err := parseMapping(string(got), "rendered")
	if err != nil {
		t.Fatalf("rendered document did not parse: %v\n%s", err, got)
	}
	provision, ok := parsed["provision"].([]any)
	if !ok || len(provision) != 1 {
		t.Fatalf("provision missing from rendered document:\n%s", got)
	}
	step, ok := provision[0].(map[string]any)
	if !ok {
		t.Fatalf("provision entry has wrong shape: %T", provision[0])
	}
	if step["script"] != script {
		t.Errorf("script did not round-trip.\ngot:  %q\nwant: %q\ndocument:\n%s", step["script"], script, got)
	}
	if step["mode"] != "system" {
		t.Errorf("mode = %v, want system", step["mode"])
	}
}

func TestRenderScalarTypesArePreserved(t *testing.T) {
	t.Parallel()

	// Values that look like other types must not be coerced. A version
	// string of "1.0" must not become a float, and "yes" must not become
	// a boolean.
	got, err := Render(RenderRequest{
		RawConfig: "cpus: 4\n",
		Overrides: "param:\n  Version: \"1.0\"\n  Enable: \"yes\"\n  Count: 3\n  Flag: true\n",
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	parsed, err := parseMapping(string(got), "rendered")
	if err != nil {
		t.Fatalf("rendered document did not parse: %v", err)
	}
	param, ok := parsed["param"].(map[string]any)
	if !ok {
		t.Fatalf("param missing:\n%s", got)
	}
	if v, ok := param["Version"].(string); !ok || v != "1.0" {
		t.Errorf("Version = %#v, want string \"1.0\"", param["Version"])
	}
	if v, ok := param["Enable"].(string); !ok || v != "yes" {
		t.Errorf("Enable = %#v, want string \"yes\"", param["Enable"])
	}
	countIsThree := false
	if v, ok := param["Count"].(uint64); ok && v == 3 {
		countIsThree = true
	}
	if v, ok := param["Count"].(int); ok && v == 3 {
		countIsThree = true
	}
	if !countIsThree {
		t.Errorf("Count = %#v, want integer 3", param["Count"])
	}
	if v, ok := param["Flag"].(bool); !ok || !v {
		t.Errorf("Flag = %#v, want bool true", param["Flag"])
	}
}

func TestRenderNoAnchorsOrAliases(t *testing.T) {
	t.Parallel()

	// Two structurally identical mounts must both be written out in full.
	// A YAML library that emitted an anchor/alias pair here would produce a
	// document Lima might interpret differently.
	shared := Mount{Location: "/same", MountPoint: "/mnt", Writable: true}
	got, err := Render(RenderRequest{Typed: InstanceConfig{
		Mounts: []Mount{shared, shared},
	}})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	s := string(got)
	if strings.Contains(s, "&") || strings.Contains(s, "*") {
		t.Errorf("rendered document contains an anchor or alias:\n%s", s)
	}
	if strings.Count(s, "/same") != 2 {
		t.Errorf("duplicate mount was collapsed:\n%s", s)
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     RenderRequest
		wantErr string
	}{
		{
			name:    "template and config are mutually exclusive",
			req:     RenderRequest{Template: "template:ubuntu", RawConfig: "cpus: 2"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "invalid raw config YAML",
			req:     RenderRequest{RawConfig: "cpus: [unclosed\n"},
			wantErr: "not valid YAML",
		},
		{
			name:    "invalid override YAML",
			req:     RenderRequest{Template: "template:ubuntu", Overrides: "\tbad: indent"},
			wantErr: "not valid YAML",
		},
		{
			name:    "raw config that is not a mapping",
			req:     RenderRequest{RawConfig: "- a\n- b\n"},
			wantErr: "must be a YAML mapping",
		},
		{
			name:    "override that is not a mapping",
			req:     RenderRequest{Template: "template:ubuntu", Overrides: "- a\n"},
			wantErr: "must be a YAML mapping",
		},
		{
			name:    "nothing at all",
			req:     RenderRequest{},
			wantErr: "empty Lima configuration",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Render(tc.req)
			if err == nil {
				t.Fatalf("Render succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRenderEmptyAndNullValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  RenderRequest
		want string
	}{
		{
			name: "empty overrides string is ignored",
			req:  RenderRequest{Template: "template:ubuntu", Overrides: "   \n  "},
			want: "base:\n- url: template:ubuntu\n",
		},
		{
			name: "empty raw config with typed values still renders",
			req:  RenderRequest{RawConfig: "", Typed: InstanceConfig{CPUs: 1}},
			want: "cpus: 1\n",
		},
		{
			// omitempty means a zero CPU count is simply absent, letting
			// Lima's own default apply.
			name: "zero typed values are omitted",
			req:  RenderRequest{Template: "template:ubuntu", Typed: InstanceConfig{CPUs: 0, Memory: ""}},
			want: "base:\n- url: template:ubuntu\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Render(tc.req)
			if err != nil {
				t.Fatalf("Render returned error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("Render() =\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestRenderDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	cfg := InstanceConfig{
		Mounts: []Mount{{Location: "/a"}},
		Base:   []BaseRef{{URL: "template:x"}},
	}
	req := RenderRequest{Typed: cfg}
	if _, err := Render(req); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Location != "/a" {
		t.Errorf("Render mutated the caller's config: %+v", cfg)
	}
}

func TestHashDocumentStability(t *testing.T) {
	t.Parallel()

	// Semantically identical inputs that differ only in whitespace and key
	// order must hash the same, or every plan would show a spurious diff.
	a, err := Render(RenderRequest{RawConfig: "cpus: 4\nmemory: 8GiB\n"})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	b, err := Render(RenderRequest{RawConfig: "memory:   8GiB\n\n\ncpus:  4\n"})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if HashDocument(a) != HashDocument(b) {
		t.Errorf("whitespace and key order changed the hash:\n%s\n---\n%s", a, b)
	}

	c, err := Render(RenderRequest{RawConfig: "cpus: 5\nmemory: 8GiB\n"})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if HashDocument(a) == HashDocument(c) {
		t.Error("a real configuration change did not move the hash")
	}
	if !strings.HasPrefix(HashDocument(a), "sha256:") {
		t.Errorf("hash %q is missing its algorithm prefix", HashDocument(a))
	}
}

func TestValidateYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty is fine", "", false},
		{"whitespace is fine", "  \n ", false},
		{"valid mapping", "cpus: 4\n", false},
		{"nested mapping", "ssh:\n  localPort: 22\n", false},
		{"sequence is not a mapping", "- a\n", true},
		{"scalar is not a mapping", "just a string\n", true},
		{"malformed", "a: [1,2\n", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateYAML(tc.input, "config")
			if tc.wantErr != (err != nil) {
				t.Errorf("ValidateYAML(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestYAMLErrorsDoNotEchoDocumentContent(t *testing.T) {
	t.Parallel()

	// A parse error must never quote the document. Raw config and overrides
	// can hold credentials or provisioning scripts, and Terraform prints
	// diagnostics to the console and to CI logs.
	secrets := []string{"hunter2", "AKIAIOSFODNN7EXAMPLE", "-----BEGIN PRIVATE KEY-----"}
	documents := []string{
		"password: hunter2\n  bad: indent\n",
		"token: AKIAIOSFODNN7EXAMPLE\nbroken: [1, 2\n",
		"key: |\n  -----BEGIN PRIVATE KEY-----\n\tbad: tab\n",
	}

	for _, doc := range documents {
		for _, field := range []string{"config", "config_overrides"} {
			err := ValidateYAML(doc, field)
			if err == nil {
				continue
			}
			for _, secret := range secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("YAML error leaked %q from the document:\n%s", secret, err)
				}
			}
			// The diagnostic must still be useful.
			if !strings.Contains(err.Error(), field) {
				t.Errorf("error %q does not name the offending field", err)
			}
		}
	}
}

func TestYAMLErrorKeepsPositionInformation(t *testing.T) {
	t.Parallel()

	// Stripping the excerpt must not strip the reason or the position.
	err := ValidateYAML("a: [1,2\n", "config")
	if err == nil {
		t.Fatal("ValidateYAML accepted malformed YAML")
	}
	if !strings.Contains(err.Error(), "not valid YAML") {
		t.Errorf("error %q lost its explanation", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error %q still contains a multi-line excerpt", err)
	}
}

func TestRenderedDocumentIsAcceptedShape(t *testing.T) {
	t.Parallel()

	// Mirrors the document proven to work against real Lima 2.2.0 during
	// discovery (see docs/development/lima-cli-contract.md §5.1).
	got, err := Render(RenderRequest{
		Template: "template:alpine",
		Typed: InstanceConfig{
			CPUs:   2,
			Memory: "1GiB",
			Disk:   "8GiB",
			Mounts: []Mount{{Location: "/private/tmp/x", Writable: false}},
			PortForwards: []PortForward{
				{GuestPort: 8080, HostPort: 18080, Proto: "tcp"},
			},
			Provision: []Provision{{Mode: "system", Script: "#!/bin/sh\necho hello\n"}},
		},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	for _, want := range []string{
		"url: template:alpine",
		"cpus: 2",
		"memory: 1GiB",
		"disk: 8GiB",
		"location: /private/tmp/x",
		"guestPort: 8080",
		"hostPort: 18080",
		"proto: tcp",
		"mode: system",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("rendered document is missing %q:\n%s", want, got)
		}
	}
}
