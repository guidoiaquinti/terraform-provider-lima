package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// Acceptance tests create real virtual machines. They run only when TF_ACC is
// set, and they refuse to run against the user's own Lima environment.
//
// # Safety
//
// Every test uses a dedicated LIMA_HOME under /tmp, created per test run and
// removed afterwards. The user's ~/.lima is never touched. The provider is
// configured with that home explicitly, so even a bug in environment handling
// cannot reach the default location.
//
// # Why /tmp and not t.TempDir()
//
// Lima builds unix socket paths as <LIMA_HOME>/<name>/ssh.sock.<16 digits> and
// enforces UNIX_PATH_MAX=104. On macOS t.TempDir() returns a path under
// /var/folders/... that is already long enough to make instance creation fail.
// This was observed directly during development; see the CLI contract §5.5.
//
// # Cost
//
// Each test boots an Alpine VM (roughly 50 MB of image, under a minute to
// boot on the development machine). The template is deliberately the smallest
// Lima offers.

const (
	// envAccHome lets CI point acceptance tests at a specific LIMA_HOME.
	envAccHome = "LIMA_PROVIDER_ACC_HOME"
	// accTemplate is the smallest template Lima ships, to keep runs cheap.
	accTemplate = "template:alpine"
)

// accHome is the shared LIMA_HOME for the whole acceptance run. It is created
// once and torn down by TestMain.
var accHome string

// skipUnlessAcc skips a test that drives Lima directly rather than through
// resource.Test, which applies the TF_ACC gate itself.
func skipUnlessAcc(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test skipped; set TF_ACC=1 to run tests that create real VMs")
	}
}

// testAccPreCheck verifies the environment before a real VM is created.
func testAccPreCheck(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("limactl"); err != nil {
		t.Fatal("acceptance tests require limactl on PATH")
	}
	if accHome == "" {
		t.Fatal("acceptance LIMA_HOME was not initialised; this is a bug in TestMain")
	}

	// Refuse to run destructive tests against a real Lima home.
	home, err := os.UserHomeDir()
	if err == nil {
		if accHome == home+"/.lima" {
			t.Fatalf("refusing to run destructive acceptance tests against the default LIMA_HOME %q", accHome)
		}
	}
}

// accProviderConfig pins the provider to the isolated LIMA_HOME.
func accProviderConfig() string {
	return fmt.Sprintf(`
provider "lima" {
  home = %q
}
`, accHome)
}

// accName builds a unique, short instance name.
//
// Short matters: the name plus LIMA_HOME must fit inside UNIX_PATH_MAX.
// Unique matters: tests must not collide when run repeatedly or in parallel.
func accName(suffix string) string {
	return fmt.Sprintf("tfa%d%s", time.Now().UnixNano()%100000, suffix)
}

// accVMType returns a VM backend this host actually supports.
//
// The suite must run on Linux CI as well as macOS, and the available backends
// differ: `vz` is macOS-only, `qemu` is everywhere. Hardcoding one made every
// test that mentions vm_type macOS-only.
func accVMType(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	info, err := accClient(t).Info(ctx)
	if err != nil {
		t.Fatalf("reading host info: %v", err)
	}
	// Prefer the native backend where there is one; it is what a user on this
	// host would get by default.
	if info.HostOS == "darwin" && lima.Contains(info.VMTypes, "vz") {
		return "vz"
	}
	if lima.Contains(info.VMTypes, "qemu") {
		return "qemu"
	}
	if len(info.VMTypes) == 0 {
		t.Fatal("Lima reports no supported VM types")
	}
	return info.VMTypes[0]
}

// accMountDir creates a real host directory to share into a guest.
//
// Created rather than hardcoded: /private/tmp exists on macOS but not on
// Linux, and Lima will not mount a location that is not there.
func accMountDir(t *testing.T, suffix string) string {
	t.Helper()
	// Kept under /tmp so the path stays short and predictable on both hosts.
	dir, err := os.MkdirTemp("/tmp", "ltfmnt")
	if err != nil {
		t.Fatalf("creating mount directory: %v", err)
	}
	path := filepath.Join(dir, suffix)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("creating mount directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return path
}

// accClient returns a client bound to the acceptance LIMA_HOME, for assertions
// that inspect Lima directly rather than through Terraform state.
func accClient(t *testing.T) lima.Client {
	t.Helper()
	c, err := lima.NewExecClient(lima.Options{Home: accHome})
	if err != nil {
		t.Fatalf("building acceptance client: %v", err)
	}
	return c
}

// destroyInstance removes an instance regardless of protection, for cleanup
// after a failed test. It is deliberately tolerant: cleanup must never mask
// the original failure.
func destroyInstance(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := accClient(t)
	if _, err := c.Inspect(ctx, name); err != nil {
		return
	}
	_ = c.Unprotect(ctx, name)
	if err := c.Delete(ctx, name); err != nil {
		t.Logf("cleanup of instance %q failed: %v", name, err)
	}
}

// checkLimaStatus asserts the real status Lima reports, independently of state.
func checkLimaStatus(t *testing.T, name string, want lima.Status) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		inst, err := accClient(t).Inspect(ctx, name)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", name, err)
		}
		if inst.Status() != want {
			return fmt.Errorf("instance %q status is %q, want %q", name, inst.Status(), want)
		}
		return nil
	}
}

// checkLimaResources asserts the resources Lima actually reports, so a test
// cannot pass on Terraform state alone.
func checkLimaResources(t *testing.T, name string, cpus, memoryBytes int64) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		inst, err := accClient(t).Inspect(ctx, name)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", name, err)
		}
		if cpus > 0 && inst.CPUs != cpus {
			return fmt.Errorf("instance %q has %d CPUs, want %d", name, inst.CPUs, cpus)
		}
		if memoryBytes > 0 && inst.MemoryBytes != memoryBytes {
			return fmt.Errorf("instance %q has %d bytes of memory, want %d", name, inst.MemoryBytes, memoryBytes)
		}
		return nil
	}
}

// checkLimaAbsent asserts the instance is gone from Lima.
func checkLimaAbsent(t *testing.T, name string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if _, err := accClient(t).Inspect(ctx, name); err == nil {
			return fmt.Errorf("instance %q still exists", name)
		} else if !lima.IsNotFound(err) {
			return fmt.Errorf("inspecting %q: %w", name, err)
		}
		return nil
	}
}

func TestAccInstanceBasic(t *testing.T) {
	name := accName("b")
	t.Cleanup(func() { destroyInstance(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
}
`, name, accTemplate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "name", name),
					resource.TestCheckResourceAttr("lima_instance.test", "instance_name", name),
					resource.TestCheckResourceAttr("lima_instance.test", "id", name),
					resource.TestCheckResourceAttr("lima_instance.test", "status", "running"),
					resource.TestCheckResourceAttr("lima_instance.test", "raw_status", "Running"),
					resource.TestCheckResourceAttr("lima_instance.test", "start", "true"),
					resource.TestCheckResourceAttr("lima_instance.test", "protect", "false"),
					resource.TestCheckResourceAttr("lima_instance.test", "hostname", "lima-"+name),
					resource.TestCheckResourceAttrSet("lima_instance.test", "ssh_address"),
					resource.TestCheckResourceAttrSet("lima_instance.test", "ssh_port"),
					resource.TestCheckResourceAttrSet("lima_instance.test", "ssh_user"),
					resource.TestCheckResourceAttrSet("lima_instance.test", "config_hash"),
					checkLimaStatus(t, name, lima.StatusRunning),
				),
			},
		},
	})
}

func TestAccInstanceStoppedThenStarted(t *testing.T) {
	name := accName("s")
	t.Cleanup(func() { destroyInstance(t, name) })

	stopped := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
}
`, name, accTemplate)

	started := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = true
}
`, name, accTemplate)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				// Creation with start = false must leave a stopped instance.
				Config: stopped,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "status", "stopped"),
					resource.TestCheckResourceAttr("lima_instance.test", "start", "false"),
					checkLimaStatus(t, name, lima.StatusStopped),
				),
			},
			{
				// start false -> true must update in place, not replace.
				Config: started,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "status", "running"),
					checkLimaStatus(t, name, lima.StatusRunning),
				),
			},
			{
				// And back again, also in place.
				Config: stopped,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "status", "stopped"),
					checkLimaStatus(t, name, lima.StatusStopped),
				),
			},
		},
	})
}

func TestAccInstanceImport(t *testing.T) {
	name := accName("i")
	t.Cleanup(func() { destroyInstance(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
  cpus     = 2
  memory   = "1GiB"
}
`, name, accTemplate),
			},
			{
				ResourceName:  "lima_instance.test",
				ImportState:   true,
				ImportStateId: name,
				// Not byte-identical: template is deliberately not
				// reconstructed, because Lima does not record it.
				ImportStateVerify: false,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d states, want 1", len(states))
					}
					s := states[0]

					// Everything Lima reports must be recorded, so a
					// configuration matching reality plans clean.
					for attr, want := range map[string]string{
						"name":    name,
						"status":  "stopped",
						"start":   "false",
						"protect": "false",
						"cpus":    "2",
						"memory":  "1GiB",
						// vm_type is host-dependent, so assert that import
						// recorded whatever Lima actually reports rather than
						// a literal that only holds on macOS.
						"vm_type": accVMType(t),
					} {
						if got := s.Attributes[attr]; got != want {
							return fmt.Errorf("imported %s = %q, want %q", attr, got, want)
						}
					}
					if s.Attributes["arch"] == "" {
						return fmt.Errorf("import did not record arch")
					}
					if s.Attributes["disk"] == "" {
						return fmt.Errorf("import did not record disk")
					}
					// Unknowable configuration must still not be invented.
					if v := s.Attributes["template"]; v != "" {
						return fmt.Errorf("import invented a template value %q", v)
					}
					return nil
				},
			},
		},
	})
}

func TestAccInstanceImportThenAdoptPlansCleanly(t *testing.T) {
	skipUnlessAcc(t)

	name := accName("ia")
	t.Cleanup(func() { destroyInstance(t, name) })

	testAccPreCheck(t)

	// Create the instance entirely outside Terraform, so nothing about it is
	// known to the provider up front.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	doc, err := lima.Render(lima.RenderRequest{
		Template: accTemplate,
		Typed:    lima.InstanceConfig{CPUs: 3, Memory: "1GiB", Disk: "9GiB"},
	})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if err := accClient(t).Create(ctx, lima.CreateRequest{Name: name, Document: doc}); err != nil {
		t.Fatalf("creating %q outside Terraform: %v", name, err)
	}

	// Declaring what the instance already is must adopt it, not rebuild it.
	adopted := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
  cpus     = 3
  memory   = "1GiB"
  disk     = "9GiB"
  vm_type  = %q
}
`, name, accTemplate, accVMType(t))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config:        adopted,
				ResourceName:  "lima_instance.test",
				ImportState:   true,
				ImportStateId: name,
				// Adopt the externally created instance into state.
				ImportStatePersist: true,
			},
			{
				// Reconciling template into state is an in-place update, and
				// must not touch the VM.
				Config: adopted,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "cpus", "3"),
					resource.TestCheckResourceAttr("lima_instance.test", "status", "stopped"),
					// The resources must be untouched: no resize, no restart.
					checkLimaResources(t, name, 3, 1<<30),
					checkLimaStatus(t, name, lima.StatusStopped),
				),
			},
			{
				// And then the plan is completely clean.
				Config:   adopted,
				PlanOnly: true,
			},
		},
	})
}

func TestAccInstanceRefreshAfterExternalStop(t *testing.T) {
	name := accName("x")
	t.Cleanup(func() { destroyInstance(t, name) })

	config := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
}
`, name, accTemplate)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Stop the instance behind Terraform's back; refresh must
				// notice and plan to start it again.
				PreConfig: func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					if err := accClient(t).Stop(ctx, name); err != nil {
						t.Fatalf("externally stopping %q: %v", name, err)
					}
				},
				// A RefreshState step reuses the previous step's config and
				// must not declare its own.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "status", "stopped"),
					resource.TestCheckResourceAttr("lima_instance.test", "start", "false"),
				),
			},
			{
				// Applying again must bring it back to running.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "status", "running"),
					checkLimaStatus(t, name, lima.StatusRunning),
				),
			},
		},
	})
}

func TestAccInstanceRefreshAfterExternalDelete(t *testing.T) {
	name := accName("d")
	t.Cleanup(func() { destroyInstance(t, name) })

	config := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
}
`, name, accTemplate)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{Config: config},
			{
				// An instance deleted outside Terraform must drop out of
				// state, producing a plan to recreate it.
				PreConfig: func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					if err := accClient(t).Delete(ctx, name); err != nil {
						t.Fatalf("externally deleting %q: %v", name, err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			{Config: config},
		},
	})
}

func TestAccInstanceProtection(t *testing.T) {
	name := accName("p")
	t.Cleanup(func() { destroyInstance(t, name) })

	protected := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
  protect  = true
}
`, name, accTemplate)

	unprotected := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
  protect  = false
}
`, name, accTemplate)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: protected,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "protect", "true"),
					func(*terraform.State) error {
						ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
						defer cancel()
						inst, err := accClient(t).Inspect(ctx, name)
						if err != nil {
							return err
						}
						if !inst.Protected {
							return fmt.Errorf("Lima does not report the instance as protected")
						}
						return nil
					},
				),
			},
			{
				// Protection must be removable in place, so the final
				// destroy can succeed.
				Config: unprotected,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("lima_instance.test", "protect", "false"),
			},
		},
	})
}

func TestAccInstanceProtectedDestroyFails(t *testing.T) {
	skipUnlessAcc(t)

	name := accName("pf")
	t.Cleanup(func() { destroyInstance(t, name) })

	testAccPreCheck(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Create and protect an instance directly, then confirm the provider
	// refuses to delete it rather than quietly unprotecting.
	c := accClient(t)
	doc, err := lima.Render(lima.RenderRequest{Template: accTemplate})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if err := c.Create(ctx, lima.CreateRequest{Name: name, Document: doc}); err != nil {
		t.Fatalf("creating %q: %v", name, err)
	}
	if err := c.Protect(ctx, name); err != nil {
		t.Fatalf("protecting %q: %v", name, err)
	}

	svc := lima.NewService(c)
	err = svc.Delete(ctx, name)
	if err == nil {
		t.Fatal("deleting a protected instance succeeded; protection must be honoured")
	}
	if !lima.IsProtected(err) {
		t.Fatalf("delete error = %v, want a protection error", err)
	}

	// The instance must still be there.
	if _, err := c.Inspect(ctx, name); err != nil {
		t.Errorf("the protected instance was removed anyway: %v", err)
	}
}

func TestAccInstanceDuplicateNameSuggestsImport(t *testing.T) {
	skipUnlessAcc(t)

	name := accName("dup")
	t.Cleanup(func() { destroyInstance(t, name) })

	testAccPreCheck(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Pre-create the instance outside Terraform.
	c := accClient(t)
	doc, err := lima.Render(lima.RenderRequest{Template: accTemplate})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if err := c.Create(ctx, lima.CreateRequest{Name: name, Document: doc}); err != nil {
		t.Fatalf("creating %q: %v", name, err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
}
`, name, accTemplate),
				// The error must point at import rather than just failing.
				ExpectError: regexpMustCompile(`(?s)already exists.*terraform import`),
			},
		},
	})
}

func TestAccInstanceImmutableAttributeForcesReplacement(t *testing.T) {
	name := accName("r")
	t.Cleanup(func() { destroyInstance(t, name) })

	// config_overrides is used rather than vm_type or arch. Those are also
	// immutable, but changing them requires a backend or emulation the host
	// may not have — a vm_type = "qemu" step fails on a machine without QEMU
	// installed, which is an environment problem rather than a provider one.
	// config_overrides forces replacement while reusing the same cached
	// image, so this test is both reliable and cheap.
	config := func(overrides string) string {
		return accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false

  config_overrides = %q
}
`, name, accTemplate, overrides)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: config("nestedVirtualization: false\n"),
			},
			{
				Config: config("nestedVirtualization: false\nmountInotify: false\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
			{
				// Re-planning the same configuration must be a no-op, so the
				// escape hatch does not itself cause a perpetual diff.
				Config:   config("nestedVirtualization: false\nmountInotify: false\n"),
				PlanOnly: true,
			},
		},
	})
}

func TestAccInstanceResizeStoppedInPlace(t *testing.T) {
	name := accName("rs")
	t.Cleanup(func() { destroyInstance(t, name) })

	config := func(cpus int, memory string) string {
		return accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
  cpus     = %d
  memory   = %q
}
`, name, accTemplate, cpus, memory)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: config(1, "1GiB"),
				Check:  checkLimaResources(t, name, 1, 1<<30),
			},
			{
				// The whole point of this roadmap item: a resource change is
				// an update, not a destroy-and-recreate.
				Config: config(2, "2GiB"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "cpus", "2"),
					resource.TestCheckResourceAttr("lima_instance.test", "memory", "2GiB"),
					resource.TestCheckResourceAttr("lima_instance.test", "status", "stopped"),
					checkLimaResources(t, name, 2, 2<<30),
				),
			},
			{
				// Re-planning the same configuration must be a no-op. This is
				// the guard against perpetual diffs.
				Config:   config(2, "2GiB"),
				PlanOnly: true,
			},
			{
				// Switching to an equivalent spelling updates the recorded
				// string but must not touch the VM: sizes are compared by
				// byte count, so no edit, stop or start is performed.
				Config: config(2, "2048MiB"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "memory", "2048MiB"),
					checkLimaResources(t, name, 2, 2<<30),
				),
			},
			{
				// And that spelling is then stable, rather than being
				// rewritten to the canonical form on every read.
				Config:   config(2, "2048MiB"),
				PlanOnly: true,
			},
		},
	})
}

func TestAccInstanceResizeRunningRestartsInPlace(t *testing.T) {
	name := accName("rr")
	t.Cleanup(func() { destroyInstance(t, name) })

	config := func(cpus int) string {
		return accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  cpus     = %d
  memory   = "1GiB"
}
`, name, accTemplate, cpus)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: config(1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "status", "running"),
					checkLimaStatus(t, name, lima.StatusRunning),
				),
			},
			{
				// Lima cannot edit a running instance, so the provider stops
				// it, applies the change and starts it again. The instance
				// must end up running with the new resources — this is the
				// "no state corruption" requirement.
				Config: config(2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "cpus", "2"),
					resource.TestCheckResourceAttr("lima_instance.test", "status", "running"),
					checkLimaStatus(t, name, lima.StatusRunning),
					checkLimaResources(t, name, 2, 1<<30),
				),
			},
		},
	})
}

func TestAccInstanceDiskGrowsInPlace(t *testing.T) {
	name := accName("dg")
	t.Cleanup(func() { destroyInstance(t, name) })

	config := func(disk string) string {
		return accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
  disk     = %q
}
`, name, accTemplate, disk)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{Config: config("8GiB")},
			{
				Config: config("12GiB"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("lima_instance.test", "disk", "12GiB"),
			},
			{
				// Shrinking is refused during planning, before anything runs,
				// because a replacement would destroy the disk's contents.
				Config:      config("4GiB"),
				PlanOnly:    true,
				ExpectError: regexpMustCompile(`(?s)Disk cannot be shrunk`),
			},
		},
	})
}

func TestAccInstanceResizeCombinedWithStop(t *testing.T) {
	name := accName("rc")
	t.Cleanup(func() { destroyInstance(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  cpus     = 1
}
`, name, accTemplate),
			},
			{
				// Resizing and stopping in one apply must not start the VM
				// again just to shut it down.
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  cpus     = 2
  start    = false
}
`, name, accTemplate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "cpus", "2"),
					resource.TestCheckResourceAttr("lima_instance.test", "status", "stopped"),
					checkLimaStatus(t, name, lima.StatusStopped),
				),
			},
		},
	})
}

func TestAccInstanceDataSource(t *testing.T) {
	name := accName("ds")
	t.Cleanup(func() { destroyInstance(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
}

data "lima_instance" "test" {
  name       = lima_instance.test.instance_name
  depends_on = [lima_instance.test]
}
`, name, accTemplate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.lima_instance.test", "name", name),
					resource.TestCheckResourceAttr("data.lima_instance.test", "status", "stopped"),
					resource.TestCheckResourceAttr("data.lima_instance.test", "protected", "false"),
					resource.TestCheckResourceAttrSet("data.lima_instance.test", "arch"),
					resource.TestCheckResourceAttrSet("data.lima_instance.test", "vm_type"),
					resource.TestCheckResourceAttrSet("data.lima_instance.test", "cpus"),
					resource.TestCheckResourceAttrSet("data.lima_instance.test", "memory"),
					resource.TestCheckResourceAttrSet("data.lima_instance.test", "disk"),
					resource.TestCheckResourceAttrSet("data.lima_instance.test", "lima_version"),
				),
			},
		},
	})
}

func TestAccHostDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + `
data "lima_host" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.lima_host.test", "lima_version"),
					resource.TestCheckResourceAttrSet("data.lima_host.test", "host_os"),
					resource.TestCheckResourceAttrSet("data.lima_host.test", "host_arch"),
					resource.TestCheckResourceAttrSet("data.lima_host.test", "binary_path"),
					resource.TestCheckResourceAttrSet("data.lima_host.test", "vm_types.#"),
					resource.TestCheckResourceAttrSet("data.lima_host.test", "templates.#"),
					// The custom LIMA_HOME must be honoured end to end.
					resource.TestCheckResourceAttr("data.lima_host.test", "lima_home", accHome),
				),
			},
		},
	})
}

func TestAccInstanceCustomHomeIsIsolated(t *testing.T) {
	name := accName("h")
	t.Cleanup(func() { destroyInstance(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false
}
`, name, accTemplate),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The instance directory must live under the isolated
					// home, proving LIMA_HOME reached limactl.
					func(*terraform.State) error {
						ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
						defer cancel()
						inst, err := accClient(t).Inspect(ctx, name)
						if err != nil {
							return err
						}
						// accHome is already symlink-resolved, so this is an
						// exact prefix comparison.
						if !strings.HasPrefix(inst.Dir, accHome+"/") {
							return fmt.Errorf("instance dir %q is not inside the acceptance LIMA_HOME %q", inst.Dir, accHome)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccInstanceMountAndPortForwardMatchWithoutFalsePositives(t *testing.T) {
	name := accName("md")
	t.Cleanup(func() { destroyInstance(t, name) })

	shared := accMountDir(t, "shared")

	config := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false

  mounts = [
    {
      location    = %q
      mount_point = "/workspace"
      writable    = true
    },
  ]

  port_forwards = [
    {
      guest_port = 8080
      host_port  = 18080
      protocol   = "tcp"
    },
  ]
}
`, name, accTemplate, shared)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "mounts.0.mount_point", "/workspace"),
					resource.TestCheckResourceAttr("lima_instance.test", "port_forwards.0.host_port", "18080"),
					// The configured entries must be found in Lima's resolved
					// configuration, alongside whatever the template added.
					checkLimaHasMount(t, name, shared, "/workspace", true),
					checkLimaHasPortForward(t, name, 8080, 18080, "tcp"),
				),
			},
			{
				// Declared mounts and port forwards that still match must not
				// produce a diff. This is the false-positive guard: a bad
				// comparison here would nag on every plan.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// checkLimaHasMount asserts against Lima's resolved configuration directly.
func checkLimaHasMount(t *testing.T, name, location, mountPoint string, writable bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		inst, err := accClient(t).Inspect(ctx, name)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", name, err)
		}
		if !inst.Config.HasMount(location, mountPoint, writable) {
			return fmt.Errorf("instance %q has no mount %s -> %s (writable=%v); resolved mounts: %+v",
				name, location, mountPoint, writable, inst.Config.Mounts)
		}
		return nil
	}
}

// checkLimaHasPortForward asserts against Lima's resolved configuration.
func checkLimaHasPortForward(t *testing.T, name string, guestPort, hostPort int64, proto string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		inst, err := accClient(t).Inspect(ctx, name)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", name, err)
		}
		if !inst.Config.HasPortForward(guestPort, hostPort, proto) {
			return fmt.Errorf("instance %q has no %s forward %d -> %d; resolved forwards: %+v",
				name, proto, guestPort, hostPort, inst.Config.PortForwards)
		}
		return nil
	}
}

// TestAccInstanceMountChangeMatchesFreshCreate is the equivalence guard for
// in-place mount reconciliation.
//
// `limactl edit --set .mounts` replaces the *already-merged* list, so the
// provider has to re-add the mounts the base template contributed itself. The
// risk is that an edited instance ends up with a different mount set than a
// freshly created one. This test creates two instances, edits one and creates
// the other directly with the target configuration, then compares what Lima
// resolved for each. They must be identical.
func TestAccInstanceMountChangeMatchesFreshCreate(t *testing.T) {
	edited := accName("me")
	fresh := accName("mf")
	t.Cleanup(func() { destroyInstance(t, edited) })
	t.Cleanup(func() { destroyInstance(t, fresh) })

	first := accMountDir(t, "first")
	second := accMountDir(t, "second")

	instance := func(name, location, mountPoint string, guestPort int) string {
		return fmt.Sprintf(`
resource "lima_instance" %q {
  name     = %q
  template = %q
  start    = false

  mounts = [
    {
      location    = %q
      mount_point = %q
      writable    = true
    },
  ]

  port_forwards = [
    {
      guest_port = %d
      host_port  = %d
    },
  ]
}
`, name, name, accTemplate, location, mountPoint, guestPort, guestPort+10000)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Start the "edited" instance from one configuration...
				Config: accProviderConfig() + instance(edited, first, "/workspace", 8080),
			},
			{
				// ...move it to the target configuration in place, and create
				// the second instance directly at that configuration.
				Config: accProviderConfig() +
					instance(edited, second, "/scratch", 9090) +
					instance(fresh, second, "/scratch", 9090),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						// The whole point: an update, not a rebuild.
						plancheck.ExpectResourceAction("lima_instance."+edited, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkResolvedConfigMatches(t, edited, fresh),
				),
			},
		},
	})
}

// checkResolvedConfigMatches asserts two instances resolved to the same mounts
// and port forwards.
func checkResolvedConfigMatches(t *testing.T, a, b string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		client := accClient(t)
		instA, err := client.Inspect(ctx, a)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", a, err)
		}
		instB, err := client.Inspect(ctx, b)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", b, err)
		}

		gotA, gotB := describeMounts(instA), describeMounts(instB)
		if gotA != gotB {
			return fmt.Errorf("edited instance resolved different mounts than a fresh create:\n edited: %s\n fresh : %s", gotA, gotB)
		}

		fwdA, fwdB := describeForwards(instA), describeForwards(instB)
		if fwdA != fwdB {
			return fmt.Errorf("edited instance resolved different port forwards than a fresh create:\n edited: %s\n fresh : %s", fwdA, fwdB)
		}
		return nil
	}
}

func describeMounts(inst lima.Instance) string {
	parts := make([]string, 0, len(inst.Config.Mounts))
	for _, m := range inst.Config.Mounts {
		parts = append(parts, fmt.Sprintf("%s->%s(w=%v)", m.Location, m.MountPoint, m.Writable))
	}
	return strings.Join(parts, ", ")
}

func describeForwards(inst lima.Instance) string {
	parts := make([]string, 0, len(inst.Config.PortForwards))
	for _, p := range inst.Config.PortForwards {
		parts = append(parts, fmt.Sprintf("%d->%d/%s", p.GuestPort, p.HostPort, p.Proto))
	}
	return strings.Join(parts, ", ")
}

func TestAccInstanceMountChangedExternallyIsRestored(t *testing.T) {
	name := accName("mx")
	t.Cleanup(func() { destroyInstance(t, name) })

	declared := accMountDir(t, "declared")
	external := accMountDir(t, "external")

	config := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false

  mounts = [
    {
      location    = %q
      mount_point = "/workspace"
      writable    = true
    },
  ]
}
`, name, accTemplate, declared)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Wipe the mounts behind Terraform's back, the way
				// `limactl edit --mount-only` would. This also removes the
				// template's mount, so restoring must not resurrect a
				// duplicate of it either.
				PreConfig: func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					empty := []lima.Mount{{Location: external, MountPoint: "/elsewhere"}}
					if err := accClient(t).Edit(ctx, name, lima.EditRequest{Mounts: &empty}); err != nil {
						t.Fatalf("externally editing mounts of %q: %v", name, err)
					}
				},
				Config: config,
				// The declared mount is gone, so applying must put it back
				// rather than failing or recreating the instance.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkLimaHasMount(t, name, declared, "/workspace", true),
					// The externally added mount is not in Terraform's
					// configuration, but it was not declared by the provider
					// either, so it is left alone rather than silently
					// removed.
					checkLimaHasMount(t, name, external, "/elsewhere", false),
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

func TestAccInstanceAdoptionRefutesWrongTemplate(t *testing.T) {
	skipUnlessAcc(t)

	name := accName("tv")
	t.Cleanup(func() { destroyInstance(t, name) })

	testAccPreCheck(t)

	// Create an Alpine instance outside Terraform, then adopt it while
	// claiming it came from Ubuntu. The provider cannot confirm a template,
	// but it can refute one whose images do not appear on the instance.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	doc, err := lima.Render(lima.RenderRequest{Template: accTemplate})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if err := accClient(t).Create(ctx, lima.CreateRequest{Name: name, Document: doc}); err != nil {
		t.Fatalf("creating %q: %v", name, err)
	}

	wrong := accProviderConfig() + fmt.Sprintf(`
resource "lima_instance" "test" {
  name     = %q
  template = "template:ubuntu"
  start    = false
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkLimaAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config:             wrong,
				ResourceName:       "lima_instance.test",
				ImportState:        true,
				ImportStateId:      name,
				ImportStatePersist: true,
			},
			{
				// The apply still succeeds — the claim is recorded, because
				// it only matters at a future replacement — but the plan
				// output carries the refutation.
				Config: wrong,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "template", "template:ubuntu"),
					// Crucially, the instance itself was not rebuilt from the
					// wrong template.
					checkLimaStatus(t, name, lima.StatusStopped),
					checkInstanceUsesAlpineImage(t, name),
				),
			},
		},
	})
}

// checkInstanceUsesAlpineImage asserts the VM still has its original image.
func checkInstanceUsesAlpineImage(t *testing.T, name string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		inst, err := accClient(t).Inspect(ctx, name)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", name, err)
		}
		for _, loc := range inst.Config.ImageLocations() {
			if strings.Contains(loc, "alpine") {
				return nil
			}
		}
		return fmt.Errorf("instance %q no longer uses an alpine image: %v", name, inst.Config.ImageLocations())
	}
}
