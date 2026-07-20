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

type Group struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	AccessFiles    AccessMethod = "files"
)

func (m AccessMethod) Valid() bool {
	return m == AccessTerminal || m == AccessDesktop || m == AccessWeb || m == AccessFiles
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
	ResourcesConfigurable           bool
	DefaultStorageBytes             int64
	InitialConnectionTimeoutSeconds int64
	StoppedRetentionSeconds         int64
	DataRetentionSeconds            int64
	Revision                        int64
	Configuration                   TemplateConfiguration
	AccessMethods                   []AccessMethod
	AllowedRoles                    []Role
	GroupAccessMode                 string
	AllowedGroupIDs                 []string
	Enabled                         bool
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
}

const (
	GroupAccessInclude = "include"
	GroupAccessExclude = "exclude"
)

type DesiredWorkspaceState string

const (
	DesiredWorkspaceStopped DesiredWorkspaceState = "stopped"
	DesiredWorkspaceRunning DesiredWorkspaceState = "running"
)

type Workspace struct {
	ID                     string
	OwnerUserID            string
	TemplateID             string
	TemplateName           string
	TemplateRevision       int64
	TemplateImageReference string
	TemplateImageDigest    string
	TemplateConfiguration  TemplateConfiguration
	// TemplateSecrets contains resolved administrator-defined secrets. It is
	// never rendered in ordinary workspace views or recorded in audit events.
	TemplateSecrets                 map[string]string
	Name                            string
	DesiredState                    DesiredWorkspaceState
	ObservedState                   string
	RuntimeID                       string
	ObservedErrorCode               string
	ObservedError                   string
	AllocatedCPUMillis              int64
	AllocatedMemoryBytes            int64
	AllocatedStorageBytes           int64
	StorageUsageBytes               int64
	StorageUsageKnown               bool
	CPUUsagePercentMilli            int64
	MemoryUsageBytes                int64
	PIDsUsage                       int64
	ResourceUsageKnown              bool
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
	UserID               string
	MaxCPUMillis         int64
	MaxMemoryBytes       int64
	MaxStorageBytes      int64
	MaxWorkspaces        int64
	MaxRunningWorkspaces int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type GroupQuota struct {
	GroupID              string
	MaxCPUMillis         int64
	MaxMemoryBytes       int64
	MaxStorageBytes      int64
	MaxWorkspaces        int64
	MaxRunningWorkspaces int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type HostSettings struct {
	ID                      int
	HostStorageBytes        int64
	CPUOverbookingFactor    float64
	MemoryOverbookingFactor float64
	ReservedStorageBytes    int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type ResourceRequest struct {
	CPUMillis    int64
	MemoryBytes  int64
	StorageBytes int64
}

type AllocationSummary struct {
	WorkspaceCount        int64
	RunningWorkspaceCount int64
	Resources             ResourceRequest
}
