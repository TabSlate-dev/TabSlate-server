-- TabSlate Server Schema — PostgreSQL 17+

CREATE TABLE IF NOT EXISTS users (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL,
    email                   TEXT NOT NULL UNIQUE,
    password_hash           TEXT NOT NULL,
    is_verified             BOOLEAN NOT NULL DEFAULT FALSE,
    verification_token      TEXT,
    verification_expires_at BIGINT,
    created_at              BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at              BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Refresh tokens for JWT rotation
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Subscription / plan per user
CREATE TABLE IF NOT EXISTS subscriptions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    plan       TEXT NOT NULL DEFAULT 'free',   -- free | pro | enterprise
    status     TEXT NOT NULL DEFAULT 'active', -- active | canceled | past_due
    expires_at BIGINT,                          -- NULL = never expires
    created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Workspaces
CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    icon       TEXT,
    color      TEXT,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Collections (bookmark folders, belong to a workspace)
CREATE TABLE IF NOT EXISTS collections (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    name         TEXT NOT NULL,
    icon         TEXT,
    position     INTEGER NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at   BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Bookmarks
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
    is_trashed    BOOLEAN NOT NULL DEFAULT FALSE,
    position      INTEGER NOT NULL DEFAULT 0,
    created_at    BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    updated_at    BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Tags
CREATE TABLE IF NOT EXISTS tags (
    id      TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT
);

-- Bookmark ↔ Tag (many-to-many)
CREATE TABLE IF NOT EXISTS bookmark_tags (
    bookmark_id TEXT NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    tag_id      TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (bookmark_id, tag_id)
);

-- Login failure tracking (for conditional captcha on login)
CREATE TABLE IF NOT EXISTS login_failures (
    email      TEXT NOT NULL,
    failed_at  BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_workspaces_user     ON workspaces(user_id);
CREATE INDEX IF NOT EXISTS idx_collections_user    ON collections(user_id);
CREATE INDEX IF NOT EXISTS idx_collections_ws      ON collections(workspace_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_user      ON bookmarks(user_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_coll      ON bookmarks(collection_id);
CREATE INDEX IF NOT EXISTS idx_tags_user           ON tags(user_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_updated   ON bookmarks(user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_workspaces_updated  ON workspaces(user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_collections_updated ON collections(user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_login_failures_email ON login_failures(email, failed_at);

-- ── Migrations ───────────────────────────────────────────────────────────────
-- These ALTER statements are idempotent: they silently succeed if the column
-- already exists (DO NOTHING on conflict for PG 17+). Wrapped in DO blocks.
DO $$ BEGIN
    ALTER TABLE users ADD COLUMN is_verified BOOLEAN NOT NULL DEFAULT FALSE;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN verification_token TEXT;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN verification_expires_at BIGINT;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;
