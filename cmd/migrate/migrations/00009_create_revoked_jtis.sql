-- +goose Up
CREATE TABLE IF NOT EXISTS revoked_jtis (
    jti TEXT PRIMARY KEY,
    exp DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_revoked_jtis_exp ON revoked_jtis(exp);

-- +goose Down
DROP INDEX IF EXISTS idx_revoked_jtis_exp;
DROP TABLE IF EXISTS revoked_jtis;