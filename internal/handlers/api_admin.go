package handlers

import (
	"errors"
	"net/http"
	"strings"

	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/httputil"
)

// The administration API is the console's "Admin" screen: identities, the
// groups and roles behind their permissions, and the per-resource grants that
// decide who may see which entry. Every write is checked in the handler as well
// as in the route, because the route can only ask about a capability — never
// about the object being changed.

// ---------------------------------------------------------------------------
// users
// ---------------------------------------------------------------------------

func (d Deps) userStore() (auth.UserStore, bool) {
	if d.Auth == nil || d.Auth.Users() == nil {
		return nil, false
	}
	return d.Auth.Users(), true
}

func (d Deps) authzStore() (authz.AuthzStore, bool) {
	if d.Authz == nil || d.Authz.Store() == nil {
		return nil, false
	}
	return d.Authz.Store(), true
}

func (d Deps) handleListUsers(w http.ResponseWriter, r *http.Request) {
	store, ok := d.userStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	users, err := store.List(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	out := make([]userPayload, 0, len(users))
	for i := range users {
		out = append(out, userView(&users[i]))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (d Deps) handleGetUser(w http.ResponseWriter, r *http.Request) {
	store, ok := d.userStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	u, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if u == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"user": userView(u)})
}

func (d Deps) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var body struct {
		Email    string   `json:"email"`
		Password string   `json:"password"`
		Name     string   `json:"name"`
		Roles    []string `json:"roles"`
		Groups   []string `json:"groups"`
	}
	if err := httputil.Decode(w, r, &body, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	u, err := d.Auth.Register(r.Context(), body.Email, body.Password, body.Name)
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	if err := d.setMembership(r, u.ID, body.Roles, body.Groups); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	u.Roles, u.Groups = body.Roles, body.Groups
	d.Log.Info("handler: user created", "user", u.ID, "by", PrincipalFrom(r).UserID)
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"user": userView(u)})
}

func (d Deps) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	store, ok := d.userStore()
	if !ok || d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	id := r.PathValue("id")
	u, err := store.Get(r.Context(), id)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if u == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var body struct {
		Email    string   `json:"email"`
		Name     string   `json:"name"`
		Password string   `json:"password"`
		Status   string   `json:"status"`
		Roles    []string `json:"roles"`
		Groups   []string `json:"groups"`
	}
	if err := httputil.Decode(w, r, &body, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if body.Email != "" {
		u.Email = auth.NormalizeEmail(body.Email)
		if !auth.ValidEmail(u.Email) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.CodeInvalidEmail)
			return
		}
	}
	if body.Name != "" {
		u.Name = strings.TrimSpace(body.Name)
	}
	if body.Password != "" {
		if err := auth.CheckPassword(body.Password); err != nil {
			writeAuthErr(w, err)
			return
		}
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			httputil.WriteErr(w, err)
			return
		}
		u.PasswordHash = hash
	}
	switch body.Status {
	case "", u.Status:
	case auth.StatusActive, auth.StatusDisabled:
		u.Status = body.Status
	default:
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return
	}
	// The last owner must stay enabled and keep a credential: disabling it
	// would leave the instance without anybody able to undo that.
	if u.IsOwner && (body.Status == auth.StatusDisabled || (body.Password == "" && u.PasswordHash == "")) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return
	}
	if err := store.Update(r.Context(), u); err != nil {
		writeAuthErr(w, err)
		return
	}
	if body.Roles != nil || body.Groups != nil {
		if err := d.setMembership(r, u.ID, body.Roles, body.Groups); err != nil {
			httputil.WriteErr(w, err)
			return
		}
		u.Roles, u.Groups = body.Roles, body.Groups
	}
	d.Log.Info("handler: user updated", "user", u.ID, "by", PrincipalFrom(r).UserID)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"user": userView(u)})
}

// handleDeleteUser disables instead of removing: the tasks, spans and grants a
// user produced are part of the audit trail and must survive it.
func (d Deps) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	store, ok := d.userStore()
	if !ok || d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	id := r.PathValue("id")
	if p := PrincipalFrom(r); p != nil && p.UserID == id {
		// Refuse the obvious footgun; the console disables the button too.
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return
	}
	u, err := store.Get(r.Context(), id)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if u == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	if u.IsOwner {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return
	}
	if err := store.Delete(r.Context(), id); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	// A disabled user keeps its rows but loses every live session: otherwise
	// "deleted" accounts would keep working until their cookie expires.
	if err := d.Auth.RevokeSession(r.Context(), id, ""); err != nil && !errors.Is(err, auth.ErrNotFound) {
		d.Log.Warn("handler: revoke sessions of disabled user", "user", id, "error", err)
	}
	d.Log.Info("handler: user disabled", "user", id, "by", PrincipalFrom(r).UserID)
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeUserSessions is the "kick this user" action.
func (d Deps) handleRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	id := r.PathValue("id")
	if err := d.Auth.RevokeSession(r.Context(), id, ""); err != nil && !errors.Is(err, auth.ErrNotFound) {
		writeAuthErr(w, err)
		return
	}
	d.Log.Info("handler: sessions revoked", "user", id, "by", PrincipalFrom(r).UserID)
	w.WriteHeader(http.StatusNoContent)
}

// setMembership replaces the roles and groups of a user. A nil slice means
// "leave alone", an empty one means "clear".
func (d Deps) setMembership(r *http.Request, userID string, roles, groups []string) error {
	store, ok := d.authzStore()
	if !ok {
		return nil
	}
	if roles != nil {
		if err := store.SetUserRoles(r.Context(), userID, roles); err != nil {
			return err
		}
	}
	if groups != nil {
		if err := store.SetUserGroups(r.Context(), userID, groups); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// roles and permissions
// ---------------------------------------------------------------------------

func (d Deps) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		// Without persistence the vocabulary is still worth rendering: the
		// console shows the matrix, it just cannot save it.
		out := make([]authz.Permission, 0, len(authz.AllPermissions))
		for _, code := range authz.AllPermissions {
			out = append(out, authz.Permission{Code: code})
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"permissions": out})
		return
	}
	perms, err := store.ListPermissions(r.Context())
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"permissions": perms})
}

func (d Deps) handleListRoles(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"roles": []authz.Role{}})
		return
	}
	roles, err := store.ListRoles(r.Context())
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (d Deps) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var role authz.Role
	if err := httputil.Decode(w, r, &role, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if err := store.CreateRole(r.Context(), &role); err != nil {
		writeAuthzErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"role": role})
}

func (d Deps) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	role := authz.Role{ID: r.PathValue("id")}
	if err := httputil.Decode(w, r, &role, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if err := store.UpdateRole(r.Context(), &role); err != nil {
		writeAuthzErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"role": role})
}

func (d Deps) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	if err := store.DeleteRole(r.Context(), r.PathValue("id")); err != nil {
		writeAuthzErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

func (d Deps) handleListGroups(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"groups": []authz.Group{}})
		return
	}
	groups, err := store.ListGroups(r.Context())
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (d Deps) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var group authz.Group
	if err := httputil.Decode(w, r, &group, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if err := store.CreateGroup(r.Context(), &group); err != nil {
		writeAuthzErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"group": group})
}

func (d Deps) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	group := authz.Group{ID: r.PathValue("id")}
	if err := httputil.Decode(w, r, &group, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if err := store.UpdateGroup(r.Context(), &group); err != nil {
		writeAuthzErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"group": group})
}

func (d Deps) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	if err := store.DeleteGroup(r.Context(), r.PathValue("id")); err != nil {
		writeAuthzErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// resource grants
// ---------------------------------------------------------------------------

func (d Deps) handleListGrants(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"grants": []authz.Grant{}})
		return
	}
	grants, err := store.ListGrants(r.Context(), r.URL.Query().Get("resource"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

func (d Deps) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var grant authz.Grant
	if err := httputil.Decode(w, r, &grant, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if err := store.CreateGrant(r.Context(), &grant); err != nil {
		writeAuthzErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"grant": grant})
}

func (d Deps) handleDeleteGrant(w http.ResponseWriter, r *http.Request) {
	store, ok := d.authzStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	if err := store.DeleteGrant(r.Context(), r.PathValue("id")); err != nil {
		writeAuthzErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAuthzErr maps authorization failures onto codes. It shares the shape of
// writeAuthErr: the console localises the code, the log keeps the detail.
func writeAuthzErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrRoleExists):
		httputil.WriteError(w, http.StatusConflict, httputil.CodeRoleExists)
	case errors.Is(err, authz.ErrGroupExists):
		httputil.WriteError(w, http.StatusConflict, httputil.CodeGroupExists)
	case errors.Is(err, authz.ErrBuiltinRole):
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeBuiltinRole)
	case errors.Is(err, authz.ErrUnknownPermission):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeUnknownPermission)
	case errors.Is(err, authz.ErrInvalidLevel), errors.Is(err, authz.ErrInvalidPrincipal):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeInvalidGrant)
	case errors.Is(err, authz.ErrRoleNotFound), errors.Is(err, authz.ErrGroupNotFound),
		errors.Is(err, authz.ErrUserNotFound), errors.Is(err, authz.ErrGrantNotFound),
		errors.Is(err, authz.ErrResourceNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
	}
}
