-- Authorization layer: permissions, roles, groups and per-resource grants.
--
-- A principal (user) collects permissions from its roles directly and from the
-- roles of every group it belongs to. Resource grants add a second axis: a
-- resource can be visible (read) or mutable (write) only for specific users or
-- groups, which is what makes "this mcp is only for that team" expressible.
CREATE TABLE IF NOT EXISTS permissions (
    code        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT ''
);

INSERT INTO permissions (code, description) VALUES
    ('resources:read', 'List and inspect registry resources'),
    ('resources:write', 'Create, update and delete registry resources'),
    ('tasks:read', 'List and inspect tasks, spans and events'),
    ('tasks:write', 'Cancel and resume tasks'),
    ('users:manage', 'Create, update, disable and delete users'),
    ('roles:manage', 'Manage groups, roles and their permissions'),
    ('grants:manage', 'Grant or revoke access to a resource'),
    ('sessions:manage', 'Revoke sessions of any user')
ON CONFLICT (code) DO NOTHING;

CREATE TABLE IF NOT EXISTS roles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    -- Built-in roles are seeded here and cannot be deleted through the API.
    builtin     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO roles (id, name, description, builtin) VALUES
    ('role-viewer', 'viewer', 'Read-only access to resources and tasks', true),
    ('role-operator', 'operator', 'Read and write access to resources and tasks', true),
    ('role-admin', 'admin', 'Full access including user and role management', true)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS role_permissions (
    role_id    TEXT NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission TEXT NOT NULL REFERENCES permissions (code) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission)
);

INSERT INTO role_permissions (role_id, permission) VALUES
    ('role-viewer', 'resources:read'),
    ('role-viewer', 'tasks:read'),
    ('role-operator', 'resources:read'),
    ('role-operator', 'resources:write'),
    ('role-operator', 'tasks:read'),
    ('role-operator', 'tasks:write'),
    ('role-admin', 'resources:read'),
    ('role-admin', 'resources:write'),
    ('role-admin', 'tasks:read'),
    ('role-admin', 'tasks:write'),
    ('role-admin', 'users:manage'),
    ('role-admin', 'roles:manage'),
    ('role-admin', 'grants:manage'),
    ('role-admin', 'sessions:manage')
ON CONFLICT (role_id, permission) DO NOTHING;

CREATE TABLE IF NOT EXISTS groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_groups (
    user_id  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_user_groups_group ON user_groups (group_id);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE IF NOT EXISTS group_roles (
    group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    role_id  TEXT NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, role_id)
);

CREATE TABLE IF NOT EXISTS resource_grants (
    id             TEXT PRIMARY KEY,
    resource_id    TEXT NOT NULL REFERENCES resources (id) ON DELETE CASCADE,
    -- user | group
    principal_kind TEXT NOT NULL,
    principal_id   TEXT NOT NULL,
    -- read | write
    level          TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (resource_id, principal_kind, principal_id, level)
);

CREATE INDEX IF NOT EXISTS idx_resource_grants_resource ON resource_grants (resource_id);
CREATE INDEX IF NOT EXISTS idx_resource_grants_principal ON resource_grants (principal_kind, principal_id);
