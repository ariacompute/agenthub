-- Registry governance: namespaces, curation state and the per-kind descriptor.
--
-- Every statement is idempotent. The columns added to resources all carry a
-- default that reproduces the behaviour of a pre-governance row: an existing
-- record stays visible (status='approved') and stays in the default namespace,
-- so an upgrade never makes a resource disappear.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS namespace    TEXT NOT NULL DEFAULT 'default';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS status       TEXT NOT NULL DEFAULT 'approved';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS source_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS kind_config  JSONB;

-- Normalise anything a previous release left empty. The column defaults cover
-- new rows; these cover rows written before the columns existed and later
-- updated through a path that wrote an empty string.
UPDATE resources SET namespace = 'default' WHERE namespace IS NULL OR namespace = '';
UPDATE resources SET status = 'approved' WHERE status IS NULL OR status = '';

CREATE INDEX IF NOT EXISTS idx_resources_namespace ON resources (namespace);
CREATE INDEX IF NOT EXISTS idx_resources_status ON resources (status);
CREATE INDEX IF NOT EXISTS idx_resources_source ON resources (source_id);

-- The dedup key: name plus version inside one namespace. The same name may be
-- reused across versions, which is what lets an upstream registry publish 1.0.0
-- and 2.0.0 of the same server.
--
-- This index is the real guarantee behind resource_already_exists. If an
-- instance already holds two rows with the same (namespace, name, version) the
-- index creation fails on purpose: the duplicates have to be resolved by a
-- human rather than silently renamed by a migration.
CREATE UNIQUE INDEX IF NOT EXISTS idx_resources_dedup ON resources (namespace, name, version);

-- Namespaces: one catalogue per team, stage or resource type. auto_approve
-- decides whether a newly registered record is discoverable at once or waits
-- for a curator.
CREATE TABLE IF NOT EXISTS registries (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    auto_approve BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The default namespace is where every pre-governance record lives, so it is
-- seeded here and can never be deleted.
INSERT INTO registries (id, name, description, auto_approve) VALUES
    ('default', 'Default', 'The namespace every record without an explicit one belongs to.', true)
ON CONFLICT (id) DO NOTHING;

-- Curation and namespace management are their own permissions rather than a
-- side effect of resources:write: being allowed to register a resource is not
-- the same as being allowed to publish it.
INSERT INTO permissions (code, description) VALUES
    ('registries:manage', 'Create, rename and delete registry namespaces'),
    ('registry:curate',   'Approve, reject and deprecate registry records'),
    ('sources:manage',    'Configure upstream registries and run their synchronisation')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission) VALUES
    ('role-admin', 'registries:manage'),
    ('role-admin', 'registry:curate'),
    ('role-admin', 'sources:manage')
ON CONFLICT (role_id, permission) DO NOTHING;
