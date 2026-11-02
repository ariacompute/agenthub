-- Reading the audit trail is its own capability: it is the one permission that
-- lets a principal see what everybody else did, so it must not ride on
-- tasks:read or users:manage.
INSERT INTO permissions (code, description) VALUES
    ('audit:read', 'Read the API audit trail and verify its hash chain')
ON CONFLICT (code) DO NOTHING;

-- The administrator role gets it by default; the platform owner holds every
-- permission regardless (authz.Principal.Can). viewer and operator do not.
INSERT INTO role_permissions (role_id, permission) VALUES
    ('role-admin', 'audit:read')
ON CONFLICT (role_id, permission) DO NOTHING;
