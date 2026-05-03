-- TabMaster Backend Schema
-- SQLite / Turso (libSQL)

CREATE TABLE IF NOT EXISTS users (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    email        TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Refresh tokens for JWT rotation
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Subscription / plan per user
CREATE TABLE IF NOT EXISTS subscriptions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    plan       TEXT NOT NULL DEFAULT 'free',    -- free | pro
    status     TEXT NOT NULL DEFAULT 'active',  -- active | canceled | past_due
    expires_at INTEGER,                          -- NULL = never expires
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Workspaces
CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    icon       TEXT,
    color      TEXT,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Collections (bookmark folders, belong to a workspace)
CREATE TABLE IF NOT EXISTS collections (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    name         TEXT NOT NULL,
    icon         TEXT,
    position     INTEGER NOT NULL DEFAULT 0,
    seq          INTEGER NOT NULL DEFAULT 0,
    deleted_at   INTEGER,
    archived_at  INTEGER,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch())
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
    is_favorite   INTEGER NOT NULL DEFAULT 0,
    is_archived   INTEGER NOT NULL DEFAULT 0,
    is_trashed    INTEGER NOT NULL DEFAULT 0,
    position      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at    INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Tags
CREATE TABLE IF NOT EXISTS tags (
    id      TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT
);

-- Bookmark <-> Tag (many-to-many)
CREATE TABLE IF NOT EXISTS bookmark_tags (
    bookmark_id TEXT NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    tag_id      TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (bookmark_id, tag_id)
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_workspaces_user    ON workspaces(user_id);
CREATE INDEX IF NOT EXISTS idx_collections_user   ON collections(user_id);
CREATE INDEX IF NOT EXISTS idx_collections_ws     ON collections(workspace_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_user     ON bookmarks(user_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_coll     ON bookmarks(collection_id);
CREATE INDEX IF NOT EXISTS idx_tags_user          ON tags(user_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_updated  ON bookmarks(user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_workspaces_updated ON workspaces(user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_collections_updated ON collections(user_id, updated_at);
