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
