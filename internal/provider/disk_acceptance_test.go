package provider

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// Disk acceptance tests are much cheaper than instance ones: creating a disk
// allocates a sparse file and downloads nothing, so these run in milliseconds.

// accDiskClient returns a disk client bound to the isolated LIMA_HOME.
func accDiskClient(t *testing.T) lima.DiskClient {
	t.Helper()
	c, err := lima.NewExecClient(lima.Options{Home: accHome})
	if err != nil {
		t.Fatalf("building acceptance disk client: %v", err)
	}
	return c
}

// destroyDisk removes a disk during cleanup, tolerating absence.
func destroyDisk(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := accDiskClient(t).DeleteDisk(ctx, name); err != nil {
		t.Logf("cleanup of disk %q failed: %v", name, err)
	}
}

// checkDiskAbsent asserts the disk is gone from Lima.
func checkDiskAbsent(t *testing.T, name string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if _, err := accDiskClient(t).InspectDisk(ctx, name); err == nil {
			return fmt.Errorf("disk %q still exists", name)
		} else if !lima.IsDiskNotFound(err) {
			return fmt.Errorf("inspecting disk %q: %w", name, err)
		}
		return nil
	}
}

// checkDiskSize asserts the size Lima reports, not just Terraform state.
func checkDiskSize(t *testing.T, name string, wantBytes int64) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		disk, err := accDiskClient(t).InspectDisk(ctx, name)
		if err != nil {
			return fmt.Errorf("inspecting disk %q: %w", name, err)
		}
		if disk.SizeBytes != wantBytes {
			return fmt.Errorf("disk %q is %d bytes, want %d", name, disk.SizeBytes, wantBytes)
		}
		return nil
	}
}

func TestAccDiskBasic(t *testing.T) {
	name := accName("d")
	t.Cleanup(func() { destroyDisk(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDiskAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "test" {
  name = %q
  size = "1GiB"
}
`, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_disk.test", "name", name),
					resource.TestCheckResourceAttr("lima_disk.test", "id", name),
					resource.TestCheckResourceAttr("lima_disk.test", "size", "1GiB"),
					resource.TestCheckResourceAttr("lima_disk.test", "mount_point", "/mnt/lima-"+name),
					resource.TestCheckResourceAttrSet("lima_disk.test", "actual_format"),
					resource.TestCheckResourceAttrSet("lima_disk.test", "dir"),
					// A fresh disk is not attached to anything.
					resource.TestCheckNoResourceAttr("lima_disk.test", "in_use_by"),
					checkDiskSize(t, name, 1<<30),
				),
			},
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "test" {
  name = %q
  size = "1GiB"
}
`, name),
				PlanOnly: true,
			},
		},
	})
}

func TestAccDiskGrowsInPlace(t *testing.T) {
	name := accName("dg")
	t.Cleanup(func() { destroyDisk(t, name) })

	config := func(size string) string {
		return accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "test" {
  name = %q
  size = %q
}
`, name, size)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDiskAbsent(t, name),
		Steps: []resource.TestStep{
			{Config: config("1GiB"), Check: checkDiskSize(t, name, 1<<30)},
			{
				// Growing must be an update, never a rebuild: replacing the
				// disk would destroy whatever is on it.
				Config: config("2GiB"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_disk.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_disk.test", "size", "2GiB"),
					checkDiskSize(t, name, 2<<30),
				),
			},
			{
				// An equivalent spelling must not resize or churn state.
				Config: config("2048MiB"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_disk.test", "size", "2048MiB"),
					checkDiskSize(t, name, 2<<30),
				),
			},
			{Config: config("2048MiB"), PlanOnly: true},
			{
				// Shrinking is refused during planning, before anything runs.
				Config:      config("512MiB"),
				PlanOnly:    true,
				ExpectError: regexpMustCompile(`(?s)Disk cannot be shrunk`),
			},
		},
	})
}

func TestAccDiskImport(t *testing.T) {
	name := accName("di")
	t.Cleanup(func() { destroyDisk(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDiskAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "test" {
  name = %q
  size = "1GiB"
}
`, name),
			},
			{
				ResourceName:  "lima_disk.test",
				ImportState:   true,
				ImportStateId: name,
				// Everything about a disk is observable except the requested
				// format, so the rest must round-trip exactly.
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"format", "timeouts"},
			},
		},
	})
}

func TestAccDiskDuplicateNameSuggestsImport(t *testing.T) {
	skipUnlessAcc(t)

	name := accName("dd")
	t.Cleanup(func() { destroyDisk(t, name) })

	testAccPreCheck(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Pre-create the disk outside Terraform.
	if err := accDiskClient(t).CreateDisk(ctx, lima.CreateDiskRequest{Name: name, SizeBytes: 1 << 30}); err != nil {
		t.Fatalf("creating disk %q: %v", name, err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "test" {
  name = %q
  size = "1GiB"
}
`, name),
				ExpectError: regexpMustCompile(`(?s)already exists.*terraform import`),
			},
		},
	})
}

func TestAccDiskDataSource(t *testing.T) {
	name := accName("ds")
	t.Cleanup(func() { destroyDisk(t, name) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDiskAbsent(t, name),
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "test" {
  name = %q
  size = "3GiB"
}

data "lima_disk" "test" {
  name       = lima_disk.test.name
  depends_on = [lima_disk.test]
}
`, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.lima_disk.test", "name", name),
					resource.TestCheckResourceAttr("data.lima_disk.test", "size", "3GiB"),
					resource.TestCheckResourceAttr("data.lima_disk.test", "size_bytes", fmt.Sprint(3<<30)),
					resource.TestCheckResourceAttrSet("data.lima_disk.test", "format"),
					resource.TestCheckResourceAttrSet("data.lima_disk.test", "mount_point"),
				),
			},
		},
	})
}

func TestAccDiskAttachedToInstance(t *testing.T) {
	diskName := accName("da")
	instName := accName("ia2")
	t.Cleanup(func() { destroyInstance(t, instName) })
	t.Cleanup(func() { destroyDisk(t, diskName) })

	attached := accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "data" {
  name = %q
  size = "1GiB"
}

resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false

  additional_disks = [lima_disk.data.name]
}
`, diskName, instName, accTemplate)

	detached := accProviderConfig() + fmt.Sprintf(`
resource "lima_disk" "data" {
  name = %q
  size = "1GiB"
}

resource "lima_instance" "test" {
  name     = %q
  template = %q
  start    = false

  additional_disks = []
}
`, diskName, instName, accTemplate)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			checkLimaAbsent(t, instName),
			checkDiskAbsent(t, diskName),
		),
		Steps: []resource.TestStep{
			{
				Config: attached,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "additional_disks.#", "1"),
					resource.TestCheckResourceAttr("lima_instance.test", "additional_disks.0", diskName),
					checkInstanceHasDisk(t, instName, diskName),
					// The instance is stopped, so Lima has not locked the disk.
					resource.TestCheckNoResourceAttr("lima_disk.data", "in_use_by"),
				),
			},
			{Config: attached, PlanOnly: true},
			{
				// Detaching is an in-place edit of the instance, not a rebuild.
				Config: detached,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("lima_instance.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("lima_instance.test", "additional_disks.#", "0"),
					checkInstanceHasNoDisks(t, instName),
				),
			},
		},
	})
}

func checkInstanceHasDisk(t *testing.T, instance, disk string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		inst, err := accClient(t).Inspect(ctx, instance)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", instance, err)
		}
		for _, n := range inst.Config.AttachedDiskNames() {
			if n == disk {
				return nil
			}
		}
		return fmt.Errorf("instance %q does not have disk %q attached; has %v",
			instance, disk, inst.Config.AttachedDiskNames())
	}
}

func checkInstanceHasNoDisks(t *testing.T, instance string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		inst, err := accClient(t).Inspect(ctx, instance)
		if err != nil {
			return fmt.Errorf("inspecting %q: %w", instance, err)
		}
		if names := inst.Config.AttachedDiskNames(); len(names) != 0 {
			return fmt.Errorf("instance %q still has disks attached: %v", instance, names)
		}
		return nil
	}
}
