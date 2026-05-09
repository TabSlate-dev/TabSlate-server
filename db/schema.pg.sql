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
    is_trashed    INT NOT NULL DEFAULT 0,
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

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN reset_otp_hash TEXT;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN reset_otp_expires_at BIGINT;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN otp_last_sent_at BIGINT;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN verification_attempts INT NOT NULL DEFAULT 0;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN reset_attempts INT NOT NULL DEFAULT 0;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE users ADD COLUMN preferences JSONB NOT NULL DEFAULT '{}';
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

-- Per-IP OTP request log (for captcha threshold enforcement)
CREATE TABLE IF NOT EXISTS otp_ip_requests (
    ip           TEXT   NOT NULL,
    requested_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);
CREATE INDEX IF NOT EXISTS idx_otp_ip_requests ON otp_ip_requests(ip, requested_at);

-- Per-IP registration log (for conditional captcha on register)
CREATE TABLE IF NOT EXISTS register_ip_requests (
    ip            TEXT   NOT NULL,
    registered_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);
CREATE INDEX IF NOT EXISTS idx_register_ip_requests ON register_ip_requests(ip, registered_at);

-- ── Sync infrastructure ──────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS user_sync_seq (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    seq     BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sse_tokens (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sse_tokens_user ON sse_tokens (user_id);

-- Add seq + deleted_at to all synced entity tables
ALTER TABLE workspaces   ADD COLUMN IF NOT EXISTS seq        BIGINT NOT NULL DEFAULT 0;
ALTER TABLE workspaces   ADD COLUMN IF NOT EXISTS deleted_at BIGINT;
ALTER TABLE collections  ADD COLUMN IF NOT EXISTS seq        BIGINT NOT NULL DEFAULT 0;
ALTER TABLE collections  ADD COLUMN IF NOT EXISTS deleted_at BIGINT;
ALTER TABLE bookmarks    ADD COLUMN IF NOT EXISTS seq        BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bookmarks    ADD COLUMN IF NOT EXISTS deleted_at BIGINT;
ALTER TABLE tags         ADD COLUMN IF NOT EXISTS seq        BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tags         ADD COLUMN IF NOT EXISTS deleted_at BIGINT;
ALTER TABLE tags         ADD COLUMN IF NOT EXISTS updated_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bookmarks    ADD COLUMN IF NOT EXISTS tag_ids    text[] NOT NULL DEFAULT '{}';
ALTER TABLE collections  ADD COLUMN IF NOT EXISTS archived_at BIGINT;

-- Delta-pull indexes: fetch all changes after a given seq for a user
CREATE INDEX IF NOT EXISTS idx_workspaces_user_seq  ON workspaces  (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_collections_user_seq ON collections (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_bookmarks_user_seq   ON bookmarks   (user_id, seq);
CREATE INDEX IF NOT EXISTS idx_tags_user_seq        ON tags        (user_id, seq);

-- ── Redis migration: drop tables now managed by Cache/Limiter ────────────────
DROP TABLE IF EXISTS sse_tokens;
DROP TABLE IF EXISTS login_failures;
DROP TABLE IF EXISTS otp_ip_requests;
DROP TABLE IF EXISTS register_ip_requests;

-- ── Saved tab groups ─────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS groups (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    color      TEXT NOT NULL,
    is_compact BOOLEAN NOT NULL DEFAULT FALSE,
    seq        BIGINT NOT NULL DEFAULT 0,
    deleted_at BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_groups_user_seq ON groups (user_id, seq);

-- Tabs within a saved group (snapshot — no individual seq)
CREATE TABLE IF NOT EXISTS group_tabs (
    id       TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    title    TEXT NOT NULL,
    url      TEXT NOT NULL,
    favicon  TEXT NOT NULL,
    position INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_group_tabs_group ON group_tabs (group_id);

-- Add workspace_id to groups (idempotent)
DO $$ BEGIN
  ALTER TABLE groups ADD COLUMN workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

-- ── Permanent deletion: integer status fields ─────────────────────────────
-- bookmarks.is_trashed: 0=active 1=trashed 2=permanently deleted
-- Cast BOOLEAN to INT (idempotent: ALTER TYPE is a no-op if already INT).
DO $$ BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bookmarks'
      AND column_name = 'is_trashed'
      AND data_type = 'boolean'
  ) THEN
    ALTER TABLE bookmarks ALTER COLUMN is_trashed TYPE INT USING (is_trashed::int);
  END IF;
END $$;

-- Migrate old "permanently deleted" bookmark records:
-- (deleted_at set, is_trashed=0) → is_trashed=2
UPDATE bookmarks SET is_trashed = 2
WHERE deleted_at IS NOT NULL AND is_trashed = 0;

-- collections.is_deleted: 0=active 1=trashed 2=permanently deleted
ALTER TABLE collections ADD COLUMN IF NOT EXISTS is_deleted INT NOT NULL DEFAULT 0;

-- Migrate existing soft-deleted collections to is_deleted=1
UPDATE collections SET is_deleted = 1
WHERE deleted_at IS NOT NULL AND is_deleted = 0;

-- groups.is_deleted: 0=active 1=trashed 2=permanently deleted
ALTER TABLE groups ADD COLUMN IF NOT EXISTS is_deleted INT NOT NULL DEFAULT 0;

-- Migrate existing soft-deleted groups to is_deleted=1
UPDATE groups SET is_deleted = 1
WHERE deleted_at IS NOT NULL AND is_deleted = 0;
