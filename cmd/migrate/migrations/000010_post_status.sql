-- +goose Up
-- posts.status — gates visibility:
--  - "draft"
--  - "published"
--  - "archived"
ALTER TABLE posts ADD COLUMN status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived'));

-- Backward compatibility for existing posts.
UPDATE posts SET status = 'published';

-- Composite index for most common queries:
--      SELECT ... WHERE status = 'published' ORDER BY created_at DESC LIMIT ?
--      SELECT ... WHERE status = ? ORDER BY created_at DESC LIMIT ?
CREATE INDEX idx_posts_status_created ON posts(status, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_posts_status_created;
ALTER TABLE posts DROP COLUMN status;