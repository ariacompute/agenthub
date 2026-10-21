package model

import "time"

// User is one identity that can authenticate: an email address plus either a
// password hash or one or more OAuth bindings (or both). Disabling a user keeps
// every row it produced — tasks, grants, sessions — so the audit trail survives.
type User struct {
	ID           string    `gorm:"primaryKey;size:64"`
	Email        string    `gorm:"size:320;uniqueIndex;not null"`
	Name         string    `gorm:"type:text;not null"`
	PasswordHash string    `gorm:"type:text;not null"`
	Status       string    `gorm:"size:32;index;not null;default:active"`
	IsOwner      bool      `gorm:"not null;default:false"`
	CreatedAt    time.Time `gorm:"index"`
	UpdatedAt    time.Time
	DisabledAt   *time.Time
}

// TableName returns the users table name.
func (User) TableName() string { return "users" }

// Session is one authenticated browser. Only the hash of the opaque cookie is
// stored, so a database leak cannot be replayed as a login.
type Session struct {
	ID         string    `gorm:"primaryKey;size:64"`
	UserID     string    `gorm:"size:64;index;not null"`
	TokenHash  string    `gorm:"size:128;uniqueIndex;not null"`
	CreatedAt  time.Time `gorm:"index"`
	ExpiresAt  time.Time `gorm:"index;not null"`
	LastSeenAt time.Time
	UserAgent  string     `gorm:"type:text"`
	IP         string     `gorm:"size:64"`
	RevokedAt  *time.Time `gorm:"index"`
}

// TableName returns the sessions table name.
func (Session) TableName() string { return "sessions" }

// OAuthAccount binds a provider identity (provider + subject) to a local user.
type OAuthAccount struct {
	ID        string    `gorm:"primaryKey;size:64"`
	UserID    string    `gorm:"size:64;index;not null"`
	Provider  string    `gorm:"size:32;not null"`
	Subject   string    `gorm:"size:256;not null"`
	Email     string    `gorm:"type:text"`
	Profile   JSONField `gorm:"type:jsonb"`
	CreatedAt time.Time
}

// TableName returns the oauth_accounts table name.
func (OAuthAccount) TableName() string { return "oauth_accounts" }

// Permission is one grantable capability, e.g. "resources:write".
type Permission struct {
	Code        string `gorm:"primaryKey;size:64"`
	Description string `gorm:"type:text"`
}

// TableName returns the permissions table name.
func (Permission) TableName() string { return "permissions" }

// Role bundles permissions; users get roles directly or through their groups.
type Role struct {
	ID          string    `gorm:"primaryKey;size:64"`
	Name        string    `gorm:"size:64;uniqueIndex;not null"`
	Description string    `gorm:"type:text"`
	Builtin     bool      `gorm:"not null;default:false"`
	CreatedAt   time.Time `gorm:"index"`
	UpdatedAt   time.Time
}

// TableName returns the roles table name.
func (Role) TableName() string { return "roles" }

// RolePermission is the role → permission edge.
type RolePermission struct {
	RoleID     string `gorm:"primaryKey;size:64"`
	Permission string `gorm:"primaryKey;size:64"`
}

// TableName returns the role_permissions table name.
func (RolePermission) TableName() string { return "role_permissions" }

// Group collects users so permissions can be granted once per team.
type Group struct {
	ID          string    `gorm:"primaryKey;size:64"`
	Name        string    `gorm:"size:64;uniqueIndex;not null"`
	Description string    `gorm:"type:text"`
	CreatedAt   time.Time `gorm:"index"`
	UpdatedAt   time.Time
}

// TableName returns the groups table name.
func (Group) TableName() string { return "groups" }

// UserGroup is the user → group edge.
type UserGroup struct {
	UserID  string `gorm:"primaryKey;size:64"`
	GroupID string `gorm:"primaryKey;size:64"`
}

// TableName returns the user_groups table name.
func (UserGroup) TableName() string { return "user_groups" }

// UserRole is the user → role edge (roles a user holds directly).
type UserRole struct {
	UserID string `gorm:"primaryKey;size:64"`
	RoleID string `gorm:"primaryKey;size:64"`
}

// TableName returns the user_roles table name.
func (UserRole) TableName() string { return "user_roles" }

// GroupRole is the group → role edge (roles inherited by every member).
type GroupRole struct {
	GroupID string `gorm:"primaryKey;size:64"`
	RoleID  string `gorm:"primaryKey;size:64"`
}

// TableName returns the group_roles table name.
func (GroupRole) TableName() string { return "group_roles" }

// APIKey is a long-lived bearer credential for programmatic access. Only the
// sha256 of the raw value (TokenHash) and a short display Prefix are stored; the
// secret itself is shown once at creation and never persisted.
type APIKey struct {
	ID         string    `gorm:"primaryKey;size:64"`
	UserID     string    `gorm:"size:64;index;not null"`
	Name       string    `gorm:"type:text;not null"`
	TokenHash  string    `gorm:"size:128;uniqueIndex;not null"`
	Prefix     string    `gorm:"size:32;not null;default:''"`
	CreatedAt  time.Time `gorm:"index"`
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time `gorm:"index"`
	CreatedBy  string     `gorm:"type:text;not null"`
}

// TableName returns the api_keys table name.
func (APIKey) TableName() string { return "api_keys" }

// APIKeyScope is the key → permission edge: the capability subset the key holds.
type APIKeyScope struct {
	KeyID      string `gorm:"primaryKey;size:64"`
	Permission string `gorm:"primaryKey;size:64"`
}

// TableName returns the api_key_scopes table name.
func (APIKeyScope) TableName() string { return "api_key_scopes" }

// ResourceGrant is the resource-level authorization: one user or group may read
// or write one registry resource. Without a grant a resource is invisible to
// that principal.
type ResourceGrant struct {
	ID            string `gorm:"primaryKey;size:64"`
	ResourceID    string `gorm:"size:64;index;not null"`
	PrincipalKind string `gorm:"size:16;index;not null"`
	PrincipalID   string `gorm:"size:64;not null"`
	Level         string `gorm:"size:16;not null"`
	CreatedAt     time.Time
}

// TableName returns the resource_grants table name.
func (ResourceGrant) TableName() string { return "resource_grants" }
