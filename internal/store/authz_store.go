package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/model"
)

// authzStore is the PostgreSQL backed authz.AuthzStore. Every mutation that
// touches more than one row runs in a transaction: a half written role (name
// updated, permissions lost) would silently widen or narrow access.
type authzStore struct{ db *gorm.DB }

// NewAuthzStore returns an authorization store backed by db.
func NewAuthzStore(db *gorm.DB) authz.AuthzStore { return &authzStore{db: db} }

// ---------------------------------------------------------------------------
// resolution
// ---------------------------------------------------------------------------

func (s *authzStore) GroupsOfUser(ctx context.Context, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	var rows []model.UserGroup
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.GroupID)
	}
	sort.Strings(out)
	return out, nil
}

// RolesOfUser returns the roles a user holds directly plus every role inherited
// from its groups; the union is what the permission lookup sees.
func (s *authzStore) RolesOfUser(ctx context.Context, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	groups, err := s.GroupsOfUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var direct []model.UserRole
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Find(&direct).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(direct))
	for _, r := range direct {
		if !seen[r.RoleID] {
			seen[r.RoleID] = true
			out = append(out, r.RoleID)
		}
	}
	if len(groups) > 0 {
		var inherited []model.GroupRole
		if err := s.db.WithContext(ctx).Where("group_id IN ?", groups).Find(&inherited).Error; err != nil {
			return nil, err
		}
		for _, r := range inherited {
			if !seen[r.RoleID] {
				seen[r.RoleID] = true
				out = append(out, r.RoleID)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *authzStore) PermissionsOfRoles(ctx context.Context, roleIDs []string) ([]string, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	var rows []model.RolePermission
	if err := s.db.WithContext(ctx).Where("role_id IN ?", roleIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if !seen[r.Permission] {
			seen[r.Permission] = true
			out = append(out, r.Permission)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *authzStore) GrantsForResource(ctx context.Context, resourceID string) ([]authz.Grant, error) {
	if resourceID == "" {
		return nil, nil
	}
	var rows []model.ResourceGrant
	if err := s.db.WithContext(ctx).Where("resource_id = ?", resourceID).Find(&rows).Error; err != nil {
		return nil, err
	}
	return grantsFromModel(rows), nil
}

func (s *authzStore) GrantsForPrincipal(ctx context.Context, userID string, groups []string) ([]authz.Grant, error) {
	if userID == "" && len(groups) == 0 {
		return nil, nil
	}
	q := s.db.WithContext(ctx).Model(&model.ResourceGrant{})
	if userID != "" && len(groups) > 0 {
		q = q.Where("principal_kind = ? AND principal_id = ?", authz.PrincipalUser, userID).
			Or("principal_kind = ? AND principal_id IN ?", authz.PrincipalGroup, groups)
	} else if userID != "" {
		q = q.Where("principal_kind = ? AND principal_id = ?", authz.PrincipalUser, userID)
	} else {
		q = q.Where("principal_kind = ? AND principal_id IN ?", authz.PrincipalGroup, groups)
	}
	var rows []model.ResourceGrant
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return grantsFromModel(rows), nil
}

// ---------------------------------------------------------------------------
// catalogue
// ---------------------------------------------------------------------------

// RestrictedResourceIDs answers "does this resource carry any grant" for a whole
// listing in one query: the caller never has to read grants it does not hold to
// learn that a resource is not public.
func (s *authzStore) RestrictedResourceIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct{ ResourceID string }
	if err := s.db.WithContext(ctx).Model(&model.ResourceGrant{}).
		Where("resource_id IN ?", ids).
		Distinct().Pluck("resource_id", &rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ResourceID] = true
	}
	return out, nil
}

func (s *authzStore) ListPermissions(ctx context.Context) ([]authz.Permission, error) {
	var rows []model.Permission
	if err := s.db.WithContext(ctx).Order("code ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]authz.Permission, 0, len(rows))
	for _, r := range rows {
		out = append(out, authz.Permission{Code: r.Code, Description: r.Description})
	}
	return out, nil
}

func (s *authzStore) ListRoles(ctx context.Context) ([]authz.Role, error) {
	var rows []model.Role
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]authz.Role, 0, len(rows))
	for i := range rows {
		out = append(out, authz.Role{
			ID:          rows[i].ID,
			Name:        rows[i].Name,
			Description: rows[i].Description,
			Builtin:     rows[i].Builtin,
			Permissions: []string{},
		})
	}
	perms, err := s.permissionsByRole(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if p, ok := perms[out[i].ID]; ok {
			out[i].Permissions = p
		}
	}
	return out, nil
}

// permissionsByRole loads every role → permission edge in one query, so listing
// roles costs two queries no matter how many roles exist.
func (s *authzStore) permissionsByRole(ctx context.Context) (map[string][]string, error) {
	var rows []model.RolePermission
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.RoleID] = append(out[r.RoleID], r.Permission)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out, nil
}

func (s *authzStore) CreateRole(ctx context.Context, r *authz.Role) error {
	if r == nil || r.Name == "" {
		return fmt.Errorf("store: create role: name is empty")
	}
	if r.ID == "" {
		r.ID = auth.NewID("role")
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := &model.Role{
			ID:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			Builtin:     r.Builtin,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Create(row).Error; err != nil {
			if isUniqueViolation(err) {
				return authz.ErrRoleExists
			}
			return err
		}
		return setRolePermissions(ctx, tx, r.ID, r.Permissions)
	})
}

func (s *authzStore) UpdateRole(ctx context.Context, r *authz.Role) error {
	if r == nil || r.ID == "" {
		return fmt.Errorf("store: update role: role or id is empty")
	}
	fields := map[string]any{"updated_at": time.Now().UTC()}
	if r.Name != "" {
		fields["name"] = r.Name
	}
	if r.Description != "" {
		fields["description"] = r.Description
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Role{}).Where("id = ?", r.ID).Updates(fields)
		if res.Error != nil {
			if isUniqueViolation(res.Error) {
				return authz.ErrRoleExists
			}
			return res.Error
		}
		if res.RowsAffected == 0 {
			return authz.ErrRoleNotFound
		}
		// A nil permission set means "leave the matrix alone": the admin screen
		// sends permissions only when the matrix tab was touched.
		if r.Permissions == nil {
			return nil
		}
		return setRolePermissions(ctx, tx, r.ID, r.Permissions)
	})
}

func (s *authzStore) DeleteRole(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.Role
		if err := tx.Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return authz.ErrRoleNotFound
			}
			return err
		}
		if row.Builtin {
			// The seeded roles are the safety net: without them a misconfigured
			// instance could end up with nobody able to grant permissions back.
			return authz.ErrBuiltinRole
		}
		if err := tx.Where("role_id = ?", id).Delete(&model.RolePermission{}).Error; err != nil {
			return err
		}
		if err := tx.Where("role_id = ?", id).Delete(&model.UserRole{}).Error; err != nil {
			return err
		}
		if err := tx.Where("role_id = ?", id).Delete(&model.GroupRole{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.Role{}).Error
	})
}

func (s *authzStore) SetRolePermissions(ctx context.Context, roleID string, permissions []string) error {
	if roleID == "" {
		return fmt.Errorf("store: set role permissions: role id is empty")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.Role{}).Where("id = ?", roleID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return authz.ErrRoleNotFound
		}
		return setRolePermissions(ctx, tx, roleID, permissions)
	})
}

// setRolePermissions replaces the edge set of one role. Unknown codes are
// rejected instead of dropped, so a typo in the matrix never silently narrows
// what a role can do.
func setRolePermissions(ctx context.Context, tx *gorm.DB, roleID string, permissions []string) error {
	if err := tx.WithContext(ctx).Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, code := range permissions {
		if !authz.ValidPermission(code) {
			return fmt.Errorf("%w: %q", authz.ErrUnknownPermission, code)
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		if err := tx.WithContext(ctx).Create(&model.RolePermission{RoleID: roleID, Permission: code}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

func (s *authzStore) ListGroups(ctx context.Context) ([]authz.Group, error) {
	var rows []model.Group
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]authz.Group, 0, len(rows))
	index := map[string]int{}
	for _, r := range rows {
		index[r.ID] = len(out)
		out = append(out, authz.Group{ID: r.ID, Name: r.Name, Description: r.Description, Roles: []string{}})
	}
	var edges []model.GroupRole
	if err := s.db.WithContext(ctx).Find(&edges).Error; err != nil {
		return nil, err
	}
	for _, e := range edges {
		if i, ok := index[e.GroupID]; ok {
			out[i].Roles = append(out[i].Roles, e.RoleID)
		}
	}
	for i := range out {
		sort.Strings(out[i].Roles)
	}
	return out, nil
}

func (s *authzStore) CreateGroup(ctx context.Context, g *authz.Group) error {
	if g == nil || g.Name == "" {
		return fmt.Errorf("store: create group: name is empty")
	}
	if g.ID == "" {
		g.ID = auth.NewID("group")
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := &model.Group{ID: g.ID, Name: g.Name, Description: g.Description, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(row).Error; err != nil {
			if isUniqueViolation(err) {
				return authz.ErrGroupExists
			}
			return err
		}
		return setGroupRoles(ctx, tx, g.ID, g.Roles)
	})
}

func (s *authzStore) UpdateGroup(ctx context.Context, g *authz.Group) error {
	if g == nil || g.ID == "" {
		return fmt.Errorf("store: update group: group or id is empty")
	}
	fields := map[string]any{"updated_at": time.Now().UTC()}
	if g.Name != "" {
		fields["name"] = g.Name
	}
	if g.Description != "" {
		fields["description"] = g.Description
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Group{}).Where("id = ?", g.ID).Updates(fields)
		if res.Error != nil {
			if isUniqueViolation(res.Error) {
				return authz.ErrGroupExists
			}
			return res.Error
		}
		if res.RowsAffected == 0 {
			return authz.ErrGroupNotFound
		}
		if g.Roles == nil {
			return nil
		}
		return setGroupRoles(ctx, tx, g.ID, g.Roles)
	})
}

func (s *authzStore) DeleteGroup(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&model.Group{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return authz.ErrGroupNotFound
		}
		if err := tx.Where("group_id = ?", id).Delete(&model.GroupRole{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&model.UserGroup{}).Error; err != nil {
			return err
		}
		// A grant pointing at a group that no longer exists would otherwise
		// linger and keep the resource restricted for nobody.
		return tx.Where("principal_kind = ? AND principal_id = ?", authz.PrincipalGroup, id).
			Delete(&model.ResourceGrant{}).Error
	})
}

func setGroupRoles(ctx context.Context, tx *gorm.DB, groupID string, roleIDs []string) error {
	if err := tx.WithContext(ctx).Where("group_id = ?", groupID).Delete(&model.GroupRole{}).Error; err != nil {
		return err
	}
	for _, roleID := range roleIDs {
		if roleID == "" {
			continue
		}
		if err := tx.WithContext(ctx).Create(&model.GroupRole{GroupID: groupID, RoleID: roleID}).Error; err != nil {
			// The FK on role_id is the guard: a role deleted between render and
			// save must fail loudly instead of leaving an orphan edge.
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// membership
// ---------------------------------------------------------------------------

func (s *authzStore) SetUserRoles(ctx context.Context, userID string, roleIDs []string) error {
	if userID == "" {
		return fmt.Errorf("store: set user roles: user id is empty")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireRoles(ctx, tx, roleIDs); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Where("user_id = ?", userID).Delete(&model.UserRole{}).Error; err != nil {
			return err
		}
		for _, roleID := range roleIDs {
			if roleID == "" {
				continue
			}
			if err := tx.WithContext(ctx).Create(&model.UserRole{UserID: userID, RoleID: roleID}).Error; err != nil {
				if isUniqueViolation(err) {
					continue
				}
				return err
			}
		}
		return nil
	})
}

func (s *authzStore) SetUserGroups(ctx context.Context, userID string, groupIDs []string) error {
	if userID == "" {
		return fmt.Errorf("store: set user groups: user id is empty")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireGroups(ctx, tx, groupIDs); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Where("user_id = ?", userID).Delete(&model.UserGroup{}).Error; err != nil {
			return err
		}
		for _, groupID := range groupIDs {
			if groupID == "" {
				continue
			}
			if err := tx.WithContext(ctx).Create(&model.UserGroup{UserID: userID, GroupID: groupID}).Error; err != nil {
				if isUniqueViolation(err) {
					continue
				}
				return err
			}
		}
		return nil
	})
}

func requireRoles(ctx context.Context, tx *gorm.DB, roleIDs []string) error {
	if len(roleIDs) == 0 {
		return nil
	}
	var n int64
	if err := tx.WithContext(ctx).Model(&model.Role{}).Where("id IN ?", roleIDs).Count(&n).Error; err != nil {
		return err
	}
	if int(n) != len(dedupe(roleIDs)) {
		return authz.ErrRoleNotFound
	}
	return nil
}

func requireGroups(ctx context.Context, tx *gorm.DB, groupIDs []string) error {
	if len(groupIDs) == 0 {
		return nil
	}
	var n int64
	if err := tx.WithContext(ctx).Model(&model.Group{}).Where("id IN ?", groupIDs).Count(&n).Error; err != nil {
		return err
	}
	if int(n) != len(dedupe(groupIDs)) {
		return authz.ErrGroupNotFound
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// resource grants
// ---------------------------------------------------------------------------

func (s *authzStore) ListGrants(ctx context.Context, resourceID string) ([]authz.Grant, error) {
	q := s.db.WithContext(ctx).Model(&model.ResourceGrant{})
	if resourceID != "" {
		q = q.Where("resource_id = ?", resourceID)
	}
	var rows []model.ResourceGrant
	if err := q.Order("resource_id ASC, principal_kind ASC, principal_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return grantsFromModel(rows), nil
}

func (s *authzStore) CreateGrant(ctx context.Context, g *authz.Grant) error {
	if g == nil || g.ResourceID == "" {
		return fmt.Errorf("store: create grant: resource id is empty")
	}
	if !authz.ValidPrincipalKind(g.PrincipalKind) {
		return authz.ErrInvalidPrincipal
	}
	if !authz.ValidLevel(g.Level) {
		return authz.ErrInvalidLevel
	}
	if g.PrincipalID == "" {
		return fmt.Errorf("store: create grant: principal id is empty")
	}
	if g.ID == "" {
		g.ID = auth.NewID("grant")
	}
	// The principal must exist: a grant to a deleted user or group would
	// restrict a resource for everyone without anyone able to lift it.
	var n int64
	missing := authz.ErrGroupNotFound
	switch g.PrincipalKind {
	case authz.PrincipalUser:
		missing = authz.ErrUserNotFound
		if err := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", g.PrincipalID).Count(&n).Error; err != nil {
			return err
		}
	default:
		if err := s.db.WithContext(ctx).Model(&model.Group{}).Where("id = ?", g.PrincipalID).Count(&n).Error; err != nil {
			return err
		}
	}
	if n == 0 {
		// A grant to a principal that no longer exists would restrict a
		// resource that nobody could ever make public again.
		return missing
	}
	var resources int64
	if err := s.db.WithContext(ctx).Model(&model.Resource{}).Where("id = ?", g.ResourceID).Count(&resources).Error; err != nil {
		return err
	}
	if resources == 0 {
		return authz.ErrResourceNotFound
	}
	row := &model.ResourceGrant{
		ID:            g.ID,
		ResourceID:    g.ResourceID,
		PrincipalKind: g.PrincipalKind,
		PrincipalID:   g.PrincipalID,
		Level:         g.Level,
		CreatedAt:     time.Now().UTC(),
	}
	// Granting the same (resource, principal, level) twice is idempotent: the
	// admin screen re-submits the whole list on save.
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "resource_id"}, {Name: "principal_kind"}, {Name: "principal_id"}, {Name: "level"}},
		DoNothing: true,
	}).Create(row).Error
}

func (s *authzStore) DeleteGrant(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.ResourceGrant{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return authz.ErrGrantNotFound
	}
	return nil
}

func grantsFromModel(rows []model.ResourceGrant) []authz.Grant {
	out := make([]authz.Grant, 0, len(rows))
	for _, r := range rows {
		out = append(out, authz.Grant{
			ID:            r.ID,
			ResourceID:    r.ResourceID,
			PrincipalKind: r.PrincipalKind,
			PrincipalID:   r.PrincipalID,
			Level:         r.Level,
		})
	}
	return out
}
