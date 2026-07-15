package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if value.Type != "bind" || value.Source != "/srv/cows-mounts/cows-workspace-123/workspace-designs-data" || !value.RemapOwnership {
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
	containerRoot := filepath.Join(root, "cows-workspace-123")
	if _, err := os.Stat(filepath.Join(containerRoot, "directory")); err != nil {
		t.Fatalf("directory mount was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cows-workspace-123", "volume")); !os.IsNotExist(err) {
		t.Fatalf("volume mount unexpectedly created a host directory: %v", err)
	}
	if err := removeMountDirectories(root, "workspace-123", mounts); err != nil {
		t.Fatalf("remove mount directories: %v", err)
	}
	if _, err := os.Stat(containerRoot); !os.IsNotExist(err) {
		t.Fatalf("directory mount was not removed: %v", err)
	}
}

func TestArchiveMountDirectoriesPreservesManagedNames(t *testing.T) {
	root := t.TempDir()
	mount := domain.TemplateMount{Name: "designs", Type: domain.TemplateMountDirectory, ContainerPath: "/designs", ReadOnly: false}
	if err := ensureMountDirectories(root, "workspace-123", []domain.TemplateMount{mount}); err != nil {
		t.Fatalf("ensure mount directory: %v", err)
	}
	managedName := mountRootName("workspace-123", mount)
	if err := os.WriteFile(filepath.Join(root, managedName, "saved.txt"), []byte("saved"), 0o600); err != nil {
		t.Fatalf("write managed data: %v", err)
	}
	archiveRoot := filepath.Join(t.TempDir(), "cows-mounts-archive")
	if err := archiveMountDirectories(root, archiveRoot, "workspace-123", []domain.TemplateMount{mount}); err != nil {
		t.Fatalf("archive mount directory: %v", err)
	}
	archived := filepath.Join(archiveRoot, "cows-workspace-123", "designs", "saved.txt")
	if value, err := os.ReadFile(archived); err != nil || string(value) != "saved" {
		t.Fatalf("archived data = %q, error = %v", value, err)
	}
	if _, err := os.Stat(filepath.Join(root, "cows-workspace-123")); !os.IsNotExist(err) {
		t.Fatalf("managed directory still exists: %v", err)
	}
}

func TestRemoveMountDirectoriesIgnoresMissingVolumeOnlyRoot(t *testing.T) {
	if err := removeMountDirectories(filepath.Join(t.TempDir(), "missing"), "workspace-123", []domain.TemplateMount{{Name: "data", Type: domain.TemplateMountVolume, ContainerPath: "/data"}}); err != nil {
		t.Fatalf("remove volume-only mounts: %v", err)
	}
}

func TestRecordArchiveActivityIncludesRecoveryIdentifiers(t *testing.T) {
	archiveRoot := t.TempDir()
	if err := recordArchiveActivity(archiveRoot, archiveActivity{
		Action:      "managed_directory_archived",
		WorkspaceID: "workspace-123",
		RuntimeID:   "container-456",
		ArchivePath: "/srv/cows-mounts-archive/cows-workspace-123",
		Status:      "succeeded",
	}); err != nil {
		t.Fatalf("record archive activity: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(archiveRoot, "archive-activity.jsonl"))
	if err != nil {
		t.Fatalf("read archive activity: %v", err)
	}
	var entry archiveActivity
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil {
		t.Fatalf("decode archive activity: %v", err)
	}
	if entry.WorkspaceID != "workspace-123" || entry.RuntimeID != "container-456" || entry.Status != "succeeded" || entry.Timestamp == "" {
		t.Fatalf("archive activity = %+v", entry)
	}
}
