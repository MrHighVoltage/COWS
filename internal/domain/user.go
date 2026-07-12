package domain

import "time"

type Role string

const (
	RoleUser          Role = "user"
	RoleAdministrator Role = "administrator"
)

func (r Role) Valid() bool {
	return r == RoleUser || r == RoleAdministrator
}

type User struct {
	ID                 string
	Username           string
	Email              string
	DisplayName        string
	Role               Role
	Disabled           bool
	MustChangePassword bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (u User) IsAdministrator() bool {
	return u.Role == RoleAdministrator && !u.Disabled
}

type Session struct {
	TokenHash string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
}

type AuditEvent struct {
	ActorUserID string
	EventType   string
	TargetType  string
	TargetID    string
	Metadata    map[string]string
	CreatedAt   time.Time
}

type AccessMethod string

const (
	AccessTerminal AccessMethod = "terminal"
	AccessDesktop  AccessMethod = "desktop"
	AccessWeb      AccessMethod = "web"
)

func (m AccessMethod) Valid() bool {
	return m == AccessTerminal || m == AccessDesktop || m == AccessWeb
}

type WorkspaceTemplate struct {
	ID                              string
	Name                            string
	Description                     string
	ImageReference                  string
	ImageDigest                     string
	DefaultCPUMillis                int64
	MaxCPUMillis                    int64
	DefaultMemoryBytes              int64
	MaxMemoryBytes                  int64
	DefaultStorageBytes             int64
	InitialConnectionTimeoutSeconds int64
	StoppedRetentionSeconds         int64
	DataRetentionSeconds            int64
	AccessMethods                   []AccessMethod
	AllowedRoles                    []Role
	Enabled                         bool
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
}

type DesiredWorkspaceState string

const (
	DesiredWorkspaceStopped DesiredWorkspaceState = "stopped"
	DesiredWorkspaceRunning DesiredWorkspaceState = "running"
)

type Workspace struct {
	ID                              string
	OwnerUserID                     string
	TemplateID                      string
	Name                            string
	DesiredState                    DesiredWorkspaceState
	ObservedState                   string
	RuntimeID                       string
	ObservedError                   string
	AllocatedCPUMillis              int64
	AllocatedMemoryBytes            int64
	AllocatedStorageBytes           int64
	InitialConnectionTimeoutSeconds int64
	StoppedRetentionSeconds         int64
	DataRetentionSeconds            int64
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
	ObservedAt                      time.Time
	StartedAt                       time.Time
	LastConnectedAt                 time.Time
	StoppedAt                       time.Time
	ContainerDeletedAt              time.Time
	DataArchiveEligibleAt           time.Time
	Operation                       string
	OperationStatus                 string
	OperationError                  string
	OperationStartedAt              time.Time
	OperationUpdatedAt              time.Time
}

type UserQuota struct {
	UserID          string
	MaxCPUMillis    int64
	MaxMemoryBytes  int64
	MaxStorageBytes int64
	MaxWorkspaces   int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type HostSettings struct {
	ID                   int
	HostStorageBytes     int64
	ReservedCPUMillis    int64
	ReservedMemoryBytes  int64
	ReservedStorageBytes int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ResourceRequest struct {
	CPUMillis    int64
	MemoryBytes  int64
	StorageBytes int64
}

type AllocationSummary struct {
	WorkspaceCount int64
	Resources      ResourceRequest
}
