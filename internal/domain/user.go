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
	ID          string
	Username    string
	DisplayName string
	Role        Role
	Disabled    bool
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
)

func (m AccessMethod) Valid() bool {
	return m == AccessTerminal || m == AccessDesktop || m == AccessWeb
}

type WorkspaceTemplate struct {
	ID                  string
	Name                string
	Description         string
	ImageReference      string
	ImageDigest         string
	DefaultCPUMillis    int64
	MaxCPUMillis        int64
	DefaultMemoryBytes  int64
	MaxMemoryBytes      int64
	DefaultStorageBytes int64
	AccessMethods       []AccessMethod
	AllowedRoles        []Role
	Enabled             bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
}
