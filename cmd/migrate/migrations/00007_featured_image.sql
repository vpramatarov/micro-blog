-- +goose Up
-- posts.featured_image_path — relative path under ./uploads of the original image (e.g. "2026/02/03/my-photo.jpg").
-- Nullable: NULL means no featured image. Variants (-s/-m/-l) are derived by suffixing this path on the client side;
-- the worker writes them to disk asynchronously.
ALTER TABLE posts ADD COLUMN featured_image_path TEXT;

-- +goose Down
ALTER TABLE posts DROP COLUMN featured_image_path;