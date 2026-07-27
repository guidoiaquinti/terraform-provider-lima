package lima_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// These tests run against a real limactl when one is installed, and skip
// otherwise. They are cheap: nothing here creates a VM, downloads an image or
// touches the user's LIMA_HOME. Their value is proving that documents the
// provider generates are accepted by Lima's own schema validator, rather than
// only by the provider's parser.

func realClient(t *testing.T) *lima.ExecClient {
	t.Helper()
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl is not installed; skipping real-CLI test")
	}
	c, err := lima.NewExecClient(lima.Options{})
	if err != nil {
		t.Skipf("could not construct a client: %v", err)
	}
	return c
}

func TestRealLimactlVersionIsSupported(t *testing.T) {
	t.Parallel()
	c := realClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	v, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version against real limactl: %v", err)
	}
	if v.Major == 0 && v.Minor == 0 {
		t.Fatalf("parsed a nonsense version from %q", v.Raw)
	}
	if _, err := lima.CheckVersion(v); err != nil {
		t.Errorf("installed Lima %s is unsupported: %v", v, err)
	}
	t.Logf("detected Lima %s", v)
}

func TestRealLimactlInfoParses(t *testing.T) {
	t.Parallel()
	c := realClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	info, err := c.Info(ctx)
	if err != nil {
		t.Fatalf("Info against real limactl: %v", err)
	}
	if info.Version == "" || info.HostOS == "" || info.HostArch == "" {
		t.Errorf("info is missing required fields: %+v", info)
	}
	if len(info.VMTypes) == 0 {
		t.Error("info reported no VM types")
	}
	if len(info.TemplateNames()) == 0 {
		t.Error("info reported no user templates")
	}
}

func TestRealLimactlAcceptsRenderedDocuments(t *testing.T) {
	t.Parallel()
	c := realClient(t)

	tests := []struct {
		name string
		req  lima.RenderRequest
	}{
		{
			name: "template only",
			req:  lima.RenderRequest{Template: "template:alpine"},
		},
		{
			name: "template with typed overrides",
			req: lima.RenderRequest{
				Template: "template:alpine",
				Typed:    lima.InstanceConfig{CPUs: 2, Memory: "1GiB", Disk: "8GiB"},
			},
		},
		{
			name: "mounts and port forwards",
			req: lima.RenderRequest{
				Template: "template:alpine",
				Typed: lima.InstanceConfig{
					// Lima refuses to mount onto a guest system path, so
					// both entries use explicit non-system mount points.
					Mounts: []lima.Mount{
						{Location: "/private/tmp", MountPoint: "/mnt/host-tmp", Writable: false},
						{Location: "/private/tmp", MountPoint: "/workspace", Writable: true},
					},
					PortForwards: []lima.PortForward{
						{GuestPort: 8080, HostPort: 18080, Proto: "tcp"},
						{GuestPort: 53, HostPort: 5353, Proto: "udp"},
					},
				},
			},
		},
		{
			name: "provisioning with special characters",
			req: lima.RenderRequest{
				Template: "template:alpine",
				Typed: lima.InstanceConfig{
					Provision: []lima.Provision{{
						Mode:   "system",
						Script: "#!/bin/sh\nset -eu\necho \"quotes 'and' \\\"more\\\"\"\nexport X='a: b'\n",
					}},
				},
			},
		},
		{
			name: "raw config with overrides",
			req: lima.RenderRequest{
				RawConfig: "base:\n- template:alpine\ncpus: 2\n",
				Overrides: "memory: 2GiB\nnestedVirtualization: false\n",
			},
		},
		{
			name: "vm type and arch",
			req: lima.RenderRequest{
				Template: "template:alpine",
				Typed:    lima.InstanceConfig{VMType: "qemu", Arch: "aarch64"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := lima.Render(tc.req)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			// limactl validate performs Lima's own schema checking without
			// creating anything.
			if err := c.Validate(ctx, doc); err != nil {
				t.Errorf("real limactl rejected the rendered document: %v\n---\n%s", err, doc)
			}
		})
	}
}

func TestRealLimactlRejectsInvalidDocument(t *testing.T) {
	t.Parallel()
	c := realClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A type error Lima's schema must catch, proving Validate surfaces real
	// failures rather than always succeeding.
	err := c.Validate(ctx, []byte("cpus: \"not-a-number\"\n"))
	if err == nil {
		t.Fatal("limactl validate accepted an invalid document")
	}

	var ce *lima.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a CommandError", err)
	}
	if ce.Message() == "" {
		t.Error("CommandError carried no message from Lima")
	}
	t.Logf("Lima reported: %s", ce.Message())
}

func TestRealLimactlResolvesTemplates(t *testing.T) {
	t.Parallel()
	c := realClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	alpine, err := c.ResolveTemplate(ctx, "template:alpine")
	if err != nil {
		t.Fatalf("resolving template:alpine: %v", err)
	}
	if len(alpine.ImageLocations()) == 0 {
		t.Fatal("template:alpine resolved to no images")
	}

	ubuntu, err := c.ResolveTemplate(ctx, "template:ubuntu")
	if err != nil {
		t.Fatalf("resolving template:ubuntu: %v", err)
	}

	// The property template verification relies on: different distributions
	// resolve to different images, so a wrong claim can be refuted.
	shared := map[string]bool{}
	for _, loc := range alpine.ImageLocations() {
		shared[loc] = true
	}
	for _, loc := range ubuntu.ImageLocations() {
		if shared[loc] {
			t.Errorf("alpine and ubuntu unexpectedly share image %q, which would make verification useless", loc)
		}
	}
	t.Logf("alpine images: %v", alpine.ImageLocations())

	// And the limitation the feature is documented against: docker is ubuntu
	// plus provisioning, so images cannot tell them apart. If this ever stops
	// being true the documentation is too pessimistic, not wrong.
	docker, err := c.ResolveTemplate(ctx, "template:docker")
	if err != nil {
		t.Fatalf("resolving template:docker: %v", err)
	}
	overlap := false
	for _, loc := range docker.ImageLocations() {
		for _, u := range ubuntu.ImageLocations() {
			if loc == u {
				overlap = true
			}
		}
	}
	if !overlap {
		t.Log("note: docker and ubuntu no longer share an image; verification is stronger than documented")
	}
}

func TestRealLimactlRejectsUnknownTemplate(t *testing.T) {
	t.Parallel()
	c := realClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := c.ResolveTemplate(ctx, "template:definitely-not-a-real-template"); err == nil {
		t.Error("resolving a non-existent template succeeded, want an error")
	}
}
