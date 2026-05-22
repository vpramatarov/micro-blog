-- +goose Up
-- Generic single-writer job queue.
-- the worker polls (no LISTEN/NOTIFY).
-- type is the dispatch key, payload is JSON the handler decodes, attempts caps retries at 3 in the worker loop.
CREATE TABLE IF NOT EXISTS jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,
    payload     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Single composite index covers the worker's pending-poll query
-- (WHERE status='pending' ORDER BY id) and the boot-time recovery query (WHERE status='running').
CREATE INDEX IF NOT EXISTS idx_jobs_status_id ON jobs(status, id);

-- +goose Down
DROP INDEX IF EXISTS idx_jobs_status_id;
DROP TABLE IF EXISTS jobs;