// Package runtime defines the COWS-facing boundary for the rootless Podman
// runtime. It deliberately contains no Podman client types.
package runtime

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"
)

var (
	ErrNotSupported = errors.New("runtime: operation not supported")
	ErrNotFound     = errors.New("runtime: workspace not found")
	ErrUnavailable  = errors.New("runtime: unavailable")
	ErrConflict     = errors.New("runtime: workspace conflict")
)

const (
	ManagedLabel      = "cows.managed"
	WorkspaceIDLabel  = "cows.workspace-id"
	RuntimeNamePodman = "podman"
)

type State string

const (
	StateUnknown State = "unknown"
	StateCreated State = "created"
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateExited  State = "exited"
	StateRemoved State = "removed"
	StateFailed  State = "failed"
)

type Capabilities struct {
	RuntimeName                   string
	SupportsResourceLimits        bool
	SupportsCPUResourceLimits     bool
	SupportsMemoryResourceLimits  bool
	SupportsPIDLimits             bool
	SupportsStorageResourceLimits bool
	SupportsPrivateNetwork        bool
	SupportsManagedLabels         bool
	SupportsPasswdEntry           bool
}

type HostCapacity struct {
	CPUMillis    int64
	MemoryBytes  int64
	StorageBytes int64
}

type CapacityProvider interface {
	HostCapacity(ctx context.Context) (HostCapacity, error)
}

type ResourceLimits struct {
	CPUMillis    int64
	MemoryBytes  int64
	PIDs         int64
	StorageBytes int64
}

// ResourceUsage is a point-in-time runtime observation. CPUPercentMilli uses
// thousandths of one percent, so 100000 means 100.000 percent.
type ResourceUsage struct {
	CPUPercentMilli int64
	MemoryBytes     int64
	PIDs            int64
	ObservedAt      time.Time
}

type Image struct {
	Reference string
	Digest    string
}

type EnvironmentVariable struct {
	Name      string
	Value     string
	Sensitive bool
}

type Mount struct {
	Name          string
	Type          string
	Source        string
	ContainerPath string
	ReadOnly      bool
	// RemapOwnership asks a runtime with user namespaces to prepare a bind
	// mount for the configured container identity. It is ignored by runtimes
	// that do not support this operation.
	RemapOwnership bool
}

type PortBinding struct {
	ServiceName   string
	Protocol      string
	ContainerPort int
	HostPort      int
	HostIP        string
}

type WorkspaceConfiguration struct {
	Command     []string
	Environment []EnvironmentVariable
	Mounts      []Mount
	Ports       []PortBinding
	User        *ContainerUser
}

// ContainerUser is fully resolved by COWS before it reaches a runtime
// adapter. The adapter decides how to express it in its native API.
type ContainerUser struct {
	Username    string
	UID         int64
	GID         int64
	Name        string
	Home        string
	Shell       string
	PasswdEntry string
}

// WorkspaceSpec is produced from a validated administrator template and COWS
// policy. It is never populated directly from browser input.
type WorkspaceSpec struct {
	WorkspaceID string
	Image       Image
	Limits      ResourceLimits
	Labels      map[string]string
	Command     []string
	Environment []EnvironmentVariable
	Mounts      []Mount
	Ports       []PortBinding
	NetworkMode string
	User        *ContainerUser
}

type WorkspaceHandle struct {
	RuntimeID   string
	WorkspaceID string
}

type ObservedWorkspace struct {
	RuntimeID   string
	WorkspaceID string
	State       State
	ExitCode    *int
	ObservedAt  time.Time
}

// Terminal is an interactive shell stream owned by a runtime adapter. The
// adapter keeps runtime-specific attach details behind this small interface.
type Terminal interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, cols, rows int) error
}

// ShellRuntime is an optional capability. Lifecycle operations do not depend
// on it, which keeps read-only and test runtimes useful without a shell.
type ShellRuntime interface {
	OpenShell(ctx context.Context, runtimeID string, command []string) (Terminal, error)
}

// InternalServiceRuntime opens a server-selected TCP service through the
// runtime's private loopback forwarding. It is intentionally not a generic
// browser proxy capability.
type InternalServiceRuntime interface {
	OpenInternalService(ctx context.Context, runtimeID string, containerPort, hostPort int) (io.ReadWriteCloser, error)
}

type FileAccessSpec struct {
	RuntimeID     string
	MountType     string
	Source        string
	ContainerPath string
	ContainerUID  int64
	ContainerGID  int64
	ReadOnly      bool
}

type FileEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// FileAccess is deliberately narrower than a general filesystem interface.
// The runtime adapter owns the storage namespace and COWS owns authorization
// and relative-path validation before invoking these methods.
type FileAccess interface {
	io.Closer
	List(ctx context.Context, relativePath string) ([]FileEntry, error)
	Usage(ctx context.Context) (int64, error)
	Stat(ctx context.Context, relativePath string) (fs.FileInfo, error)
	OpenRead(ctx context.Context, relativePath string) (io.ReadCloser, fs.FileInfo, error)
	OpenZip(ctx context.Context, relativePath string) (io.ReadCloser, error)
	CreateDirectory(ctx context.Context, relativePath, name string) error
	Delete(ctx context.Context, relativePath string) error
	Rename(ctx context.Context, relativePath, oldName, newName string) error
	Upload(ctx context.Context, relativePath, filename string, source io.Reader) error
}

type FileAccessRuntime interface {
	OpenFileAccess(ctx context.Context, spec FileAccessSpec) (FileAccess, error)
}

type StorageUsageSpec struct {
	RuntimeID string
	Mounts    []FileAccessSpec
}

type StorageUsageRuntime interface {
	WorkspaceStorageUsage(ctx context.Context, spec StorageUsageSpec) (int64, error)
}

// ResourceUsageRuntime is optional so lifecycle-only test runtimes and future
// adapters can be used without pretending to provide live statistics.
type ResourceUsageRuntime interface {
	WorkspaceResourceUsage(ctx context.Context, runtimeID string) (ResourceUsage, error)
}

type Runtime interface {
	Name(ctx context.Context) (string, error)
	Capabilities(ctx context.Context) (Capabilities, error)
	ListManaged(ctx context.Context) ([]ObservedWorkspace, error)
	CreateWorkspace(ctx context.Context, spec WorkspaceSpec) (WorkspaceHandle, error)
	StartWorkspace(ctx context.Context, runtimeID string) error
	StopWorkspace(ctx context.Context, runtimeID string, timeout time.Duration) error
	RemoveWorkspace(ctx context.Context, runtimeID string) error
	InspectWorkspace(ctx context.Context, runtimeID string) (ObservedWorkspace, error)
}

func ManagedLabels(workspaceID string) map[string]string {
	return map[string]string{ManagedLabel: "true", WorkspaceIDLabel: workspaceID}
}
