package handlers

import (
	"errors"
	"net/http"
	"time"

	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/httputil"
)

// API keys: long-lived bearer credentials for programmatic access. The routes
// mirror the users/backups admin endpoints, but every write branches on whether
// the caller holds apikeys:manage — that permission may list and act on anyone's
// keys, while a plain user may only reach their own. A key is always issued with
// a permission subset of the caller's own, so it can never escalate.

func (d Deps) apiKeyStore() (auth.APIKeyStore, bool) {
	if d.Auth == nil || d.Auth.APIKeys() == nil {
		return nil, false
	}
	return d.Auth.APIKeys(), true
}

// apiKeyView is the console-facing shape of a key. The secret hash never leaves
// the store; only the display prefix and the scopes do.
type apiKeyView struct {
	ID         string     `json:"id"`
	UserID     string     `json:"userId"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	Revoked    bool       `json:"revoked"`
	CreatedBy  string     `json:"createdBy"`
}

func keyView(k *auth.APIKey) apiKeyView {
	return apiKeyView{
		ID:         k.ID,
		UserID:     k.UserID,
		Name:       k.Name,
		Prefix:     k.Prefix,
		Scopes:     k.Scopes,
		CreatedAt:  k.CreatedAt,
		LastUsedAt: k.LastUsedAt,
		ExpiresAt:  k.ExpiresAt,
		Revoked:    k.RevokedAt != nil,
		CreatedBy:  k.CreatedBy,
	}
}

// handleListAPIKeys returns the caller's own keys, or every key when the caller
// holds apikeys:manage.
func (d Deps) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	store, ok := d.apiKeyStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	p := PrincipalFrom(r)
	keys, err := func() ([]auth.APIKey, error) {
		if p.Can(authz.PermAPIKeysManage) {
			return store.ListAll(r.Context())
		}
		return store.ListForUser(r.Context(), p.UserID)
	}()
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	out := make([]apiKeyView, 0, len(keys))
	for i := range keys {
		out = append(out, keyView(&keys[i]))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"apiKeys": out})
}

// handleCreateAPIKey mints a new key. Any signed-in user may create their own;
// creating for another user requires apikeys:manage. The scopes are clamped to
// what the caller may do, so a key can never carry more power than its issuer.
func (d Deps) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	store, ok := d.apiKeyStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	p := PrincipalFrom(r)
	var body struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		UserID        string   `json:"userId"`
		ExpiresInDays *int     `json:"expiresInDays"`
	}
	if err := httputil.Decode(w, r, &body, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	target := p.UserID
	if body.UserID != "" && body.UserID != p.UserID {
		if !p.Can(authz.PermAPIKeysManage) {
			httputil.WriteError(w, http.StatusForbidden, httputil.CodeForbidden)
			return
		}
		target = body.UserID
	}
	if body.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeAPIKeyNameRequired)
		return
	}
	if err := auth.ValidateAPIKeyScopes(body.Scopes, p.Can); err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	raw, prefix, hash, err := auth.NewAPIKeyToken()
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	key := &auth.APIKey{
		ID:        auth.NewID("key"),
		UserID:    target,
		Name:      body.Name,
		Prefix:    prefix,
		Scopes:    body.Scopes,
		CreatedBy: p.UserID,
	}
	if body.ExpiresInDays != nil && *body.ExpiresInDays > 0 {
		expiry := time.Now().UTC().AddDate(0, 0, *body.ExpiresInDays)
		key.ExpiresAt = &expiry
	}
	if err := store.Create(r.Context(), key, hash); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	d.Log.Info("handler: api key created", "key", key.ID, "user", target, "by", p.UserID)
	// The raw value is returned exactly once; the store only ever holds the hash.
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"apiKey": keyView(key), "token": raw})
}

// handleGetAPIKey returns one key the caller may see.
func (d Deps) handleGetAPIKey(w http.ResponseWriter, r *http.Request) {
	store, ok := d.apiKeyStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	key, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if key == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeAPIKeyNotFound)
		return
	}
	if !d.mayManageKey(PrincipalFrom(r), key) {
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeForbidden)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"apiKey": keyView(key)})
}

// handleUpdateAPIKey renames a key, moves its expiry, or replaces its scopes.
// Scope changes are clamped to the caller's permissions, exactly like creation.
func (d Deps) handleUpdateAPIKey(w http.ResponseWriter, r *http.Request) {
	store, ok := d.apiKeyStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	p := PrincipalFrom(r)
	key, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if key == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeAPIKeyNotFound)
		return
	}
	if !d.mayManageKey(p, key) {
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeForbidden)
		return
	}
	var body struct {
		Name      *string    `json:"name"`
		ExpiresAt *time.Time `json:"expiresAt"`
		Scopes    []string   `json:"scopes"`
	}
	if err := httputil.Decode(w, r, &body, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if body.Scopes != nil {
		if err := auth.ValidateAPIKeyScopes(body.Scopes, p.Can); err != nil {
			writeAPIKeyErr(w, err)
			return
		}
		if err := store.SetScopes(r.Context(), key.ID, body.Scopes); err != nil {
			httputil.WriteErr(w, err)
			return
		}
		key.Scopes = body.Scopes
	}
	if body.Name != nil {
		key.Name = *body.Name
	}
	if body.ExpiresAt != nil {
		key.ExpiresAt = body.ExpiresAt
	}
	if err := store.Update(r.Context(), key); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"apiKey": keyView(key)})
}

// handleRevokeAPIKey soft-deletes a key so it stops authenticating but keeps its
// row for the audit trail.
func (d Deps) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	store, ok := d.apiKeyStore()
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	p := PrincipalFrom(r)
	key, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if key == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeAPIKeyNotFound)
		return
	}
	if !d.mayManageKey(p, key) {
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeForbidden)
		return
	}
	if err := store.Revoke(r.Context(), key.ID); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	d.Log.Info("handler: api key revoked", "key", key.ID, "by", p.UserID)
	w.WriteHeader(http.StatusNoContent)
}

// mayManageKey reports whether the principal may read or change a key: owners of
// the key, and holders of apikeys:manage.
func (d Deps) mayManageKey(p *authz.Principal, key *auth.APIKey) bool {
	if p == nil {
		return false
	}
	return p.UserID == key.UserID || p.Can(authz.PermAPIKeysManage)
}

// writeAPIKeyErr maps the api key domain errors onto their machine readable
// codes, mirroring writeAuthErr. The sentinels are plain errors (not
// *httputil.Error) so WriteErr cannot classify them on its own.
func writeAPIKeyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrAPIKeyNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeAPIKeyNotFound)
	case errors.Is(err, auth.ErrAPIKeyNameRequired):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeAPIKeyNameRequired)
	case errors.Is(err, auth.ErrAPIKeyInvalidScope):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeAPIKeyInvalidScope)
	case errors.Is(err, auth.ErrAPIKeyExpired):
		httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeAPIKeyExpired)
	case errors.Is(err, auth.ErrAPIKeyRevoked):
		httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeAPIKeyRevoked)
	default:
		httputil.WriteErr(w, err)
	}
}
