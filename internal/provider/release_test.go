// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Terraform Registry reads the wire protocol version from
// terraform-registry-manifest.json. Getting it wrong or omitting it does not fail
// any build or any test that exercises the provider — it fails every
// `terraform init` against the *published* provider, which is the worst possible
// place to discover it. This provider is built on terraform-plugin-framework,
// which speaks protocol 6.
func TestRegistryManifestDeclaresProtocolSix(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "terraform-registry-manifest.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\n\nThe registry requires this file; without it a published release is uninstallable.", path, err)
	}

	var manifest struct {
		Version  int `json:"version"`
		Metadata struct {
			ProtocolVersions []string `json:"protocol_versions"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}

	if manifest.Version != 1 {
		t.Errorf("manifest version = %d, want 1", manifest.Version)
	}
	if got := manifest.Metadata.ProtocolVersions; len(got) != 1 || got[0] != "6.0" {
		t.Errorf("protocol_versions = %v, want [\"6.0\"]: terraform-plugin-framework serves protocol 6", got)
	}
}

// GoReleaser has to publish the manifest as a release asset under the name the
// registry looks for and include that published name in SHA256SUMS. Having the
// file in the repository or uploading it without a checksum is not enough: the
// registry rejects the whole version when any release asset is unchecksummed.
func TestGoreleaserPublishesAndChecksumsTheRegistryManifest(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yml"))
	if err != nil {
		t.Fatalf("reading .goreleaser.yml: %v", err)
	}
	config := string(body)

	const source = "glob: terraform-registry-manifest.json"
	if got := strings.Count(config, source); got != 2 {
		t.Errorf(".goreleaser.yml contains %q %d times, want 2: once under checksum.extra_files and once under release.extra_files", source, got)
	}

	const publishedName = `name_template: "{{ .ProjectName }}_{{ .Version }}_manifest.json"`
	if got := strings.Count(config, publishedName); got != 2 {
		t.Errorf(".goreleaser.yml contains %q %d times, want 2: the checksum entry and release asset must use the same registry filename", publishedName, got)
	}
}

// The provider address is spelled out in main.go and the Makefile, and the two
// must agree or `make install` puts the binary somewhere Terraform will not look
// for it.
func TestProviderAddressAgreesWithTheMakefile(t *testing.T) {
	t.Parallel()

	main, err := os.ReadFile(filepath.Join("..", "..", "main.go"))
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}

	const wantAddress = "registry.terraform.io/guidoiaquinti/lima"
	if !strings.Contains(string(main), wantAddress) {
		t.Errorf("main.go does not serve %q", wantAddress)
	}

	// The Makefile builds the same address from three variables.
	for _, want := range []string{
		"HOSTNAME    := registry.terraform.io",
		"NAMESPACE   := guidoiaquinti",
		"NAME        := lima",
	} {
		if !strings.Contains(string(makefile), want) {
			t.Errorf("Makefile does not define %q, so its plugin path would disagree with main.go", want)
		}
	}
}
