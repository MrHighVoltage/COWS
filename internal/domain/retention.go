package domain

import "time"

// RetainedWorkspaceVolume preserves ownership metadata after its workspace row
// is deleted. It does not grant authority to mount, restore, or remove a volume.
type RetainedWorkspaceVolume struct {
	VolumeName    string
	WorkspaceID   string
	OwnerUserID   string
	TemplateID    string
	MountName     string
	ContainerPath string
	ReadOnly      bool
	RetainedAt    time.Time
}
