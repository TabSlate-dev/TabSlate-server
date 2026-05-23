-- TabSlate Server Schema — PostgreSQL 17+

CREATE TABLE IF NOT EXISTS users (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL,
    email                   TEXT NOT NULL UNIQUE,
    password_hash           TEXT NOT NULL,
    is_verified             BOOLEAN NOT NULL DEFAULT FALSE,
    verification_token      TEXT,
    verification_expires_at BIGINT,
    verification_attempts   INT NOT NULL DEFAULT 0,
    reset_otp_hash          TEXT,
    reset_otp_expires_at    BIGINT,
    reset_attempts          INT NOT NULL DEFAULT 0,
    otp_last_sent_at        BIGINT,
    preferences             JSONB NOT NULL DEFAULT '{}',
    billing_synced_at       BIGINT,
    created_at              BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at              BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Backfill: existing rows keep billing_synced_at = NULL (re-sync will be attempted on next
-- GET /auth/me call, which is correct — the sync may genuinely be incomplete for old rows).
ALTER TABLE users ADD COLUMN IF NOT EXISTS billing_synced_at BIGINT;

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    plan       TEXT NOT NULL DEFAULT 'free',
    status     TEXT NOT NULL DEFAULT 'active',
    expires_at BIGINT,
    created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE IF NOT EXISTS user_sync_seq (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    seq     BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    icon       TEXT,
    color      TEXT,
    position   INTEGER NOT NULL DEFAULT 0,
    seq        BIGINT NOT NULL DEFAULT 0,
    deleted_at BIGINT,
    created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE IF NOT EXISTS collections (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    name         TEXT NOT NULL,
    icon         TEXT,
    position     INTEGER NOT NULL DEFAULT 0,
    seq          BIGINT NOT NULL DEFAULT 0,
    deleted_at   BIGINT,
    archived_at  BIGINT,
    is_deleted   INT NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at   BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE IF NOT EXISTS bookmarks (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    collection_id TEXT REFERENCES collections(id) ON DELETE SET NULL,
    title         TEXT NOT NULL,
    url           TEXT NOT NULL,
    favicon_url   TEXT,
    description   TEXT,
    is_favorite   BOOLEAN NOT NULL DEFAULT FALSE,
    is_archived   BOOLEAN NOT NULL DEFAULT FALSE,
    is_trashed    INT NOT NULL DEFAULT 0,
    tag_ids       TEXT[] NOT NULL DEFAULT '{}',
    position      INTEGER NOT NULL DEFAULT 0,
    seq           BIGINT NOT NULL DEFAULT 0,
    deleted_at    BIGINT,
    created_at    BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at    BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE IF NOT EXISTS tags (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    color      TEXT,
    seq        BIGINT NOT NULL DEFAULT 0,
    deleted_at BIGINT,
    updated_at BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS bookmark_tags (
    bookmark_id TEXT NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    tag_id      TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (bookmark_id, tag_id)
);

CREATE TABLE IF NOT EXISTS groups (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    color        TEXT NOT NULL,
    is_compact   BOOLEAN NOT NULL DEFAULT FALSE,
    seq          BIGINT NOT NULL DEFAULT 0,
    deleted_at   BIGINT,
    is_deleted   INT NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS group_tabs (
    id       TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    title    TEXT NOT NULL,
    url      TEXT NOT NULL,
    favicon  TEXT NOT NULL,
    position INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS subscription_capacity (
    plan_code        TEXT         PRIMARY KEY,
    plan_id          TEXT         NOT NULL DEFAULT '',
    max_workspaces   INTEGER      NOT NULL DEFAULT -1,
    max_bookmarks    INTEGER      NOT NULL DEFAULT -1,
    max_collections  INTEGER      NOT NULL DEFAULT -1,
    max_tags         INTEGER      NOT NULL DEFAULT -1,
    max_saved_groups INTEGER      NOT NULL DEFAULT -1,
    trash_grace_days INTEGER      NOT NULL DEFAULT 7,
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
ALTER TABLE subscription_capacity ADD COLUMN IF NOT EXISTS max_saved_groups INTEGER NOT NULL DEFAULT -1;
ALTER TABLE subscription_capacity ADD COLUMN IF NOT EXISTS trash_grace_days INTEGER NOT NULL DEFAULT 7;

-- Historical trashed collections with deleted_at set and archived_at NULL were
-- stored as is_deleted = 0 due to a frontend bug. Promote them to trashed so
-- the cleanup goroutine can auto-expire them. This backfill is idempotent.
UPDATE collections
SET is_deleted = 1
WHERE is_deleted = 0 AND deleted_at IS NOT NULL AND archived_at IS NULL;

-- Indexes
CREATE INDEX IF NOT EXISTS idx_workspaces_user       ON workspaces  (user_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_updated    ON workspaces  (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_workspaces_user_seq   ON workspaces  (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_collections_user      ON collections (user_id);
CREATE INDEX IF NOT EXISTS idx_collections_ws        ON collections (workspace_id);
CREATE INDEX IF NOT EXISTS idx_collections_updated   ON collections (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_collections_user_seq  ON collections (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_bookmarks_user        ON bookmarks   (user_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_coll        ON bookmarks   (collection_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_updated     ON bookmarks   (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_bookmarks_user_seq    ON bookmarks   (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_tags_user             ON tags        (user_id);
CREATE INDEX IF NOT EXISTS idx_tags_user_seq         ON tags        (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_groups_user_seq       ON groups      (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_group_tabs_group      ON group_tabs  (group_id);
