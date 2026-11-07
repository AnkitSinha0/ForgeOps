-- 0002_api_keys: credentials for the control plane API, and an index for keyset pages.

CREATE TABLE api_keys (
    id         uuid        PRIMARY KEY,
    tenant_id  uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- The first characters of the key, so a person can tell keys apart in a list.
    prefix     text        NOT NULL,
    -- SHA-256 of the key. The key itself is never stored: a leaked table holds
    -- nothing that can be sent back to the API.
    key_hash   bytea       NOT NULL UNIQUE CHECK (octet_length(key_hash) = 32),
    created_at timestamptz NOT NULL,
    -- Set on revoke rather than deleting the row, so the history survives.
    revoked_at timestamptz
);
CREATE INDEX api_keys_tenant_created ON api_keys (tenant_id, created_at DESC, id DESC);

-- Keyset pagination sorts by (created_at DESC, id DESC), because two rows can share a
-- timestamp. The old index covered only created_at, so ties needed an extra sort.
DROP INDEX deployments_project_created;
CREATE INDEX deployments_project_created_id ON deployments (project_id, created_at DESC, id DESC);
