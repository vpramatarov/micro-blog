-- +goose Up
-- Comments on posts. Single-level threading: parent_id NULL = top-level, non-NULL = reply to a top-level comment. 
CREATE TABLE IF NOT EXISTS comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    parent_id INTEGER NULL,
    content TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (post_id) REFERENCES posts(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES comments(id) ON DELETE CASCADE
);

-- post_id serves the top-level list filter AND the correlated comment_count subquery that runs on every post read.
CREATE INDEX IF NOT EXISTS idx_comments_post ON comments(post_id);
CREATE INDEX IF NOT EXISTS idx_comments_parent ON comments(parent_id);

-- Per-post comment toggle. Writable through POST/PUT /admin/posts, gated by the existing post:edit permission (Admin/Editor: all, Author: own).
ALTER TABLE posts ADD COLUMN comments_enabled INTEGER NOT NULL DEFAULT 1; -- enabled by default

-- Generic key/value runtime settings. First row is the global comments kill-switch: 
--   - '0' closes commenting on every post regardless of the per-post flag (even for Admin); 
--   - '1' defers to posts.comments_enabled.
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO settings (key, value) VALUES ('comments_enabled', '1'); -- enabled by default

-- +goose Down
DROP TABLE IF EXISTS settings;
ALTER TABLE posts DROP COLUMN comments_enabled;
DROP INDEX IF EXISTS idx_comments_parent;
DROP INDEX IF EXISTS idx_comments_post;
DROP TABLE IF EXISTS comments;
