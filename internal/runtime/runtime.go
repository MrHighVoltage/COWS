// Package runtime defines the COWS-facing boundary for container runtimes.
//
// This package deliberately contains no Docker or Podman client. Adapters will
// be added only after the lifecycle and reconciliation semantics are reviewed.
package runtime

import (
	"context"
	"errors"
	"io"
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
	RuntimeNameDocker = "docker"
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
	ContainerPath string
	ReadOnly      bool
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
