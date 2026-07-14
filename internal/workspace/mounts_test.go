package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cows-project/cows/internal/domain"
)

func TestMaterializeMountUsesManagedVolumeAndDirectoryNames(t *testing.T) {
	mount := domain.TemplateMount{Name: "designs", Type: domain.TemplateMountVolume, ContainerPath: "/foss/designs", NamePrefix: "workspace-", NameSuffix: "-data"}
	value, err := materializeMount(t.TempDir(), "workspace-123", mount)
	if err != nil {
		t.Fatalf("materialize volume: %v", err)
	}
	if value.Type != domain.TemplateMountVolume || value.Source != "cows-workspace-123-workspace-designs-data" {
		t.Fatalf("unexpected volume mount: %+v", value)
	}

	mount.Type = domain.TemplateMountDirectory
	value, err = materializeMount("/srv/cows-mounts", "workspace-123", mount)
	if err != nil {
		t.Fatalf("materialize directory: %v", err)
	}
	if value.Type != "bind" || value.Source != "/srv/cows-mounts/cows-workspace-123-workspace-designs-data" {
		t.Fatalf("unexpected directory mount: %+v", value)
	}
}

func TestEnsureMountDirectoriesCreatesOnlyDirectoryMounts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mounts")
	mounts := []domain.TemplateMount{
		{Name: "volume", Type: domain.TemplateMountVolume, ContainerPath: "/volume"},
		{Name: "directory", Type: domain.TemplateMountDirectory, ContainerPath: "/directory"},
	}
	if err := ensureMountDirectories(root, "workspace-123", mounts); err != nil {
		t.Fatalf("ensure mount directories: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cows-workspace-123-directory")); err != nil {
		t.Fatalf("directory mount was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cows-workspace-123-volume")); !os.IsNotExist(err) {
		t.Fatalf("volume mount unexpectedly created a host directory: %v", err)
	}
	if err := removeMountDirectories(root, "workspace-123", mounts); err != nil {
		t.Fatalf("remove mount directories: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cows-workspace-123-directory")); !os.IsNotExist(err) {
		t.Fatalf("directory mount was not removed: %v", err)
	}
}

func TestRemoveMountDirectoriesIgnoresMissingVolumeOnlyRoot(t *testing.T) {
	if err := removeMountDirectories(filepath.Join(t.TempDir(), "missing"), "workspace-123", []domain.TemplateMount{{Name: "data", Type: domain.TemplateMountVolume, ContainerPath: "/data"}}); err != nil {
		t.Fatalf("remove volume-only mounts: %v", err)
	}
}
