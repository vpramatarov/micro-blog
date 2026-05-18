-- +goose Up
-- Categories: editorial taxonomy. One row per category, name UNIQUE so the
-- admin/editor UI can refer to them by name. id=1 'Uncategorized' is the default for the FK added to posts below.
CREATE TABLE IF NOT EXISTS categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO categories (id, name) VALUES (1, 'Uncategorized');

-- Tags: editorial taxonomy. M:N with posts via post_tags below.
CREATE TABLE IF NOT EXISTS tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- post_tags M:N join. Both FKs CASCADE so deleting a post or a tag cleans the
-- join rows automatically. (post_id is half of the composite PK, which gives
-- us an implicit index for forward lookups; the explicit index on tag_id below covers the reverse direction.)
CREATE TABLE IF NOT EXISTS post_tags (
    post_id INTEGER NOT NULL,
    tag_id INTEGER NOT NULL,
    PRIMARY KEY (post_id, tag_id),
    FOREIGN KEY (post_id) REFERENCES posts(id) ON DELETE CASCADE,
    FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_post_tags_tag ON post_tags(tag_id);

-- posts.category_id — required, defaults to 1 ('Uncategorized') so existing
-- rows backfill cleanly. ON DELETE RESTRICT means deleting a category with
-- attached posts is refused at the SQL layer; the handler surfaces this as a 409 category_in_use.
ALTER TABLE posts ADD COLUMN category_id INTEGER NOT NULL DEFAULT 1 REFERENCES categories(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_posts_category ON posts(category_id);

-- posts.slug — auto-generated server-side from the title via internal/slug.
-- Backfilled to 'post-<id>' for any rows that existed before this migration
-- (covers test/dev DBs; production deployments at v0.1 had no posts yet).
-- UNIQUE index is the source of truth for collision detection; the handler
-- uses repo.FindAvailableSlug to pick an available variant before insert.
ALTER TABLE posts ADD COLUMN slug TEXT NOT NULL DEFAULT '';
UPDATE posts SET slug = 'post-' || id WHERE slug = '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_posts_slug ON posts(slug);

-- +goose Down
DROP INDEX IF EXISTS idx_posts_slug;
ALTER TABLE posts DROP COLUMN slug;
DROP INDEX IF EXISTS idx_posts_category;
ALTER TABLE posts DROP COLUMN category_id;
DROP INDEX IF EXISTS idx_post_tags_tag;
DROP TABLE IF EXISTS post_tags;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS categories;
