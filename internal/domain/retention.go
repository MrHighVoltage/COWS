package domain

import "time"

// RetainedWorkspaceVolume preserves ownership metadata after its workspace row
// is deleted. On its own it does not grant authority to mount, restore, or
// remove a volume; the self-service and administrator recovery paths each
// verify ownership and consume the record independently (see decision 0025).
type RetainedWorkspaceVolume struct {
	VolumeName    string
	WorkspaceID   string
	OwnerUserID   string
	TemplateID    string
	TemplateName  string
	WorkspaceName string
	MountName     string
	ContainerPath string
	ReadOnly      bool
	RetainedAt    time.Time
}

// RetainedDirectoryMount snapshots one archived directory-type mount so a
// later restore knows its logical mount name and container path without
// re-deriving them from the template, which may have changed since.
type RetainedDirectoryMount struct {
	Name          string `json:"name"`
	NamePrefix    string `json:"name_prefix,omitempty"`
	NameSuffix    string `json:"name_suffix,omitempty"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
}

// RetainedWorkspaceDirectory preserves ownership metadata for an archived
// per-container directory tree after its workspace row is deleted. The
// archive itself lives on the host filesystem under the archive root; this
// record is what makes it discoverable and attributable to an owner. It does
// not by itself grant authority to restore the archive into a new workspace.
type RetainedWorkspaceDirectory struct {
	WorkspaceID   string
	OwnerUserID   string
	TemplateID    string
	TemplateName  string
	WorkspaceName string
	ArchivePath   string
	Mounts        []RetainedDirectoryMount
	RetainedAt    time.Time
}
