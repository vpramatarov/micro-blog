-- +goose Up
-- categories.slug — auto-generated server-side from the title via internal/slug.
-- uses repo.FindAvailableSlug to pick an available variant before insert.
ALTER TABLE categories ADD COLUMN slug TEXT NOT NULL DEFAULT '';
UPDATE categories SET slug = 'category-' || id WHERE slug = '';
UPDATE categories SET slug = 'uncategorized' WHERE id = 1;

-- tags.slug — auto-generated server-side from the title via internal/slug.
-- uses repo.FindAvailableSlug to pick an available variant before insert.
ALTER TABLE tags ADD COLUMN slug TEXT NOT NULL DEFAULT '';
UPDATE tags SET slug = 'tag-' || id WHERE slug = '';

-- Add UNIQUE index to slug columns:
CREATE UNIQUE INDEX IF NOT EXISTS idx_categories_slug ON categories(slug);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tags_slug ON tags(slug);

-- +goose Down
DROP INDEX IF EXISTS idx_categories_slug;
DROP INDEX IF EXISTS idx_tags_slug;
ALTER TABLE categories DROP COLUMN slug;
ALTER TABLE tags DROP COLUMN slug;