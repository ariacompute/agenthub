// Package authz is the authorization layer of agenthub.
//
// It answers two questions: "what may this principal do" (permissions, resolved
// from the roles a user holds directly plus the roles of every group it belongs
// to) and "may this principal touch this resource" (resource level grants). The
// storage it needs is declared here as AuthzStore and implemented by
// internal/store, so this package never imports GORM.
//
// A resource is unrestricted while it has no grant rows at all: anyone holding
// resources:read sees it. The first grant makes it restricted, and from then on
// only the granted users/groups (and the owner) can see or change it. That rule
// is what makes existing registries keep working after the grants table appears.
package authz

import (
	"context"
	"errors"
	"sort"
)

// Errors the authorization layer reports. Handlers turn them into machine
// readable codes, so nothing here carries operator facing prose.
var (
	ErrRoleNotFound      = errors.New("authz: role not found")
	ErrGroupNotFound     = errors.New("authz: group not found")
	ErrUserNotFound      = errors.New("authz: user not found")
	ErrGrantNotFound     = errors.New("authz: grant not found")
	ErrResourceNotFound  = errors.New("authz: resource not found")
	ErrRoleExists        = errors.New("authz: role name already in use")
	ErrGroupExists       = errors.New("authz: group name already in use")
	ErrBuiltinRole       = errors.New("authz: built-in role cannot be deleted")
	ErrUnknownPermission = errors.New("authz: unknown permission code")
	ErrInvalidLevel      = errors.New("authz: grant level must be read or write")
	ErrInvalidPrincipal  = errors.New("authz: principal kind must be user or group")
)

// ValidLevel reports whether level is one of the grant levels.
func ValidLevel(level string) bool {
	return level == LevelRead || level == LevelWrite
}

// ValidPrincipalKind reports whether kind addresses a user or a group.
func ValidPrincipalKind(kind string) bool {
	return kind == PrincipalUser || kind == PrincipalGroup
}

// ValidPermission reports whether code is part of the permission vocabulary.
func ValidPermission(code string) bool {
	for _, p := range AllPermissions {
		if p == code {
			return true
		}
	}
	return false
}

// Permission codes. They are the vocabulary of the admin permission matrix and
// the only thing handlers ask about; the front end renders them per language
// from enums-copy.
const (
	PermResourcesRead  = "resources:read"
	PermResourcesWrite = "resources:write"
	PermTasksRead      = "tasks:read"
	PermTasksWrite     = "tasks:write"
	PermUsersManage    = "users:manage"
	PermRolesManage    = "roles:manage"
	PermGrantsManage   = "grants:manage"
	PermSessionsManage = "sessions:manage"
	PermBackupsManage  = "backups:manage"
	// Registry governance. Registering a resource and publishing it are
	// different acts, so they are different permissions: resources:write says
	// "you may add a record", registry:curate says "you may decide whether
	// anyone can find it".
	PermRegistriesManage = "registries:manage"
	PermRegistryCurate   = "registry:curate"
	PermSourcesManage    = "sources:manage"
	// API keys. Issuing or revoking a key is its own permission so a key can
	// read and act but never silently mint more keys with powers it lacks.
	PermAPIKeysManage = "apikeys:manage"
)

// AllPermissions is the full grantable set, in the order the admin matrix shows
// them.
var AllPermissions = []string{
	PermResourcesRead,
	PermResourcesWrite,
	PermTasksRead,
	PermTasksWrite,
	PermUsersManage,
	PermRolesManage,
	PermGrantsManage,
	PermSessionsManage,
	PermBackupsManage,
	PermRegistriesManage,
	PermRegistryCurate,
	PermSourcesManage,
	PermAPIKeysManage,
}

// Principal kinds and grant levels of a ResourceGrant.
const (
	PrincipalUser  = "user"
	PrincipalGroup = "group"
	LevelRead      = "read"
	LevelWrite     = "write"
)

// Principal is the authenticated identity carried in the request context. It is
// resolved once per request: permissions, groups and roles are read together so
// handlers never fan out per check.
type Principal struct {
	UserID      string          `json:"userId"`
	Email       string          `json:"email"`
	Name        string          `json:"name"`
	IsOwner     bool            `json:"isOwner"`
	Permissions map[string]bool `json:"permissions,omitempty"`
	Roles       []string        `json:"roles,omitempty"`
	Groups      []string        `json:"groups,omitempty"`
}

// Can reports whether the principal holds perm. The bootstrap owner holds every
// known permission implicitly, so an instance can never lock itself out; a code
// outside the vocabulary is never granted, so a typo cannot become power.
func (p *Principal) Can(perm string) bool {
	if p == nil {
		return false
	}
	if p.IsOwner {
		return ValidPermission(perm)
	}
	return p.Permissions[perm]
}

// Anonymous reports whether the request carried no session at all.
func (p *Principal) Anonymous() bool { return p == nil || p.UserID == "" }

// DevPrincipal is the identity used when no authentication service is wired in
// (unit tests, in-memory mode). It holds every permission and therefore sees
// every resource: exactly the open behaviour agenthub had before identities
// existed, so a harness without users keeps working.
func DevPrincipal() *Principal {
	perms := map[string]bool{}
	for _, c := range AllPermissions {
		perms[c] = true
	}
	return &Principal{UserID: "dev", Email: "dev@localhost", Name: "dev", IsOwner: true, Permissions: perms}
}

// AnonymousPrincipal is an unauthenticated caller. It may read the public
// catalogue (the Agent Card and MCP export), which is what an agent that has
// never logged in is allowed to discover: resources without grants are public.
func AnonymousPrincipal() *Principal {
	return &Principal{Permissions: map[string]bool{PermResourcesRead: true}}
}

// Group, Role and Permission are the shapes the admin API returns: plain values,
// free of persistence tags.
// The JSON tags are part of the console contract: the admin screen reads
// camelCase fields, so a renamed Go field must never change the wire.
type Group struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Roles       []string `json:"roles"`
}

// Role is a named bundle of permissions.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Builtin     bool     `json:"builtin"`
	Permissions []string `json:"permissions"`
}

// Permission is one grantable capability.
type Permission struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Grant is a resource level authorization.
type Grant struct {
	ID            string `json:"id"`
	ResourceID    string `json:"resourceId"`
	PrincipalKind string `json:"principalKind"`
	PrincipalID   string `json:"principalId"`
	Level         string `json:"level"`
}

// AuthzStore is the port the authorization layer needs; internal/store provides
// the GORM implementation and main wires it in.
type AuthzStore interface {
	// Resolution.
	GroupsOfUser(ctx context.Context, userID string) ([]string, error)
	RolesOfUser(ctx context.Context, userID string) ([]string, error)
	PermissionsOfRoles(ctx context.Context, roleIDs []string) ([]string, error)
	GrantsForResource(ctx context.Context, resourceID string) ([]Grant, error)
	GrantsForPrincipal(ctx context.Context, userID string, groups []string) ([]Grant, error)
	// RestrictedResourceIDs reports which of the given ids carry at least one
	// grant. A caller must not have to see other people's grants to learn that
	// a resource is not public.
	RestrictedResourceIDs(ctx context.Context, ids []string) (map[string]bool, error)

	// Catalogues.
	ListPermissions(ctx context.Context) ([]Permission, error)
	ListRoles(ctx context.Context) ([]Role, error)
	CreateRole(ctx context.Context, r *Role) error
	UpdateRole(ctx context.Context, r *Role) error
	DeleteRole(ctx context.Context, id string) error
	SetRolePermissions(ctx context.Context, roleID string, permissions []string) error

	ListGroups(ctx context.Context) ([]Group, error)
	CreateGroup(ctx context.Context, g *Group) error
	UpdateGroup(ctx context.Context, g *Group) error
	DeleteGroup(ctx context.Context, id string) error

	// Membership.
	SetUserRoles(ctx context.Context, userID string, roleIDs []string) error
	SetUserGroups(ctx context.Context, userID string, groupIDs []string) error

	// Resource grants. resourceID "" lists every grant.
	ListGrants(ctx context.Context, resourceID string) ([]Grant, error)
	CreateGrant(ctx context.Context, g *Grant) error
	DeleteGrant(ctx context.Context, id string) error
}

// Service resolves principals and resource access.
type Service struct {
	store AuthzStore
}

// New builds the authorization service on top of a store (nil keeps it inert,
// which is what unit tests use to exercise the pure rules).
func New(store AuthzStore) *Service { return &Service{store: store} }

// Store exposes the underlying port for the admin handlers.
func (s *Service) Store() AuthzStore {
	if s == nil {
		return nil
	}
	return s.store
}

// Load resolves everything a request needs to know about a user: the groups it
// belongs to, the roles that gives it, and the permissions those roles carry.
func (s *Service) Load(ctx context.Context, userID, email, name string, isOwner bool) (*Principal, error) {
	// A nil service is what an identity layer without authorization (tests,
	// in-memory mode) carries: the principal is still built, it just holds only
	// what the owner flag gives it.
	if s == nil || s.store == nil {
		return &Principal{
			UserID:      userID,
			Email:       email,
			Name:        name,
			IsOwner:     isOwner,
			Permissions: map[string]bool{},
		}, nil
	}
	p := &Principal{
		UserID:      userID,
		Email:       email,
		Name:        name,
		IsOwner:     isOwner,
		Permissions: map[string]bool{},
	}
	if s.store == nil {
		return p, nil
	}
	groups, err := s.store.GroupsOfUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	roles, err := s.store.RolesOfUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	perms, err := s.store.PermissionsOfRoles(ctx, roles)
	if err != nil {
		return nil, err
	}
	sort.Strings(groups)
	sort.Strings(roles)
	sort.Strings(perms)
	p.Groups, p.Roles = groups, roles
	for _, c := range perms {
		p.Permissions[c] = true
	}
	return p, nil
}

// CanAccessResource reports whether p may read (or write) one resource.
//
// An owner always can. Otherwise: a resource with no grants at all is open to
// anyone holding the matching permission; a restricted one needs a grant that
// covers the requested level.
func (s *Service) CanAccessResource(ctx context.Context, p *Principal, resourceID string, write bool) (bool, error) {
	perm := PermResourcesRead
	if write {
		perm = PermResourcesWrite
	}
	if !p.Can(perm) {
		return false, nil
	}
	if s == nil || s.store == nil || p.IsOwner {
		return true, nil
	}
	grants, err := s.store.GrantsForResource(ctx, resourceID)
	if err != nil {
		return false, err
	}
	// No grants recorded: the resource is unrestricted.
	if len(grants) == 0 {
		return true, nil
	}
	level := LevelRead
	if write {
		level = LevelWrite
	}
	groups := map[string]bool{}
	for _, g := range p.Groups {
		groups[g] = true
	}
	for _, g := range grants {
		hit := (g.PrincipalKind == PrincipalUser && g.PrincipalID == p.UserID) ||
			(g.PrincipalKind == PrincipalGroup && groups[g.PrincipalID])
		if !hit {
			continue
		}
		if g.Level == LevelWrite || (g.Level == LevelRead && level == LevelRead) {
			return true, nil
		}
	}
	return false, nil
}

// VisibleResourceIDs returns the resources p may see, as resourceID → level. A
// resource without grants is visible to anyone with resources:read; otherwise it
// appears only when a grant covers the principal.
func (s *Service) VisibleResourceIDs(ctx context.Context, p *Principal, all []string) (map[string]string, error) {
	out := make(map[string]string, len(all))
	if !p.Can(PermResourcesRead) {
		return out, nil
	}
	if s == nil || s.store == nil || p.IsOwner {
		for _, id := range all {
			out[id] = LevelWrite
		}
		return out, nil
	}
	restricted, err := s.store.RestrictedResourceIDs(ctx, all)
	if err != nil {
		return nil, err
	}
	grants, err := s.store.GrantsForPrincipal(ctx, p.UserID, p.Groups)
	if err != nil {
		return nil, err
	}
	allowed := map[string]string{}
	for _, g := range grants {
		// The highest level any grant gives this principal wins.
		if g.Level == LevelWrite || allowed[g.ResourceID] != LevelWrite {
			allowed[g.ResourceID] = g.Level
		}
	}
	for _, id := range all {
		if !restricted[id] {
			out[id] = LevelWrite // unrestricted: whatever the permission allows
			continue
		}
		if lvl, ok := allowed[id]; ok {
			out[id] = lvl
		}
	}
	return out, nil
}
