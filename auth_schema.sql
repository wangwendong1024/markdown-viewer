-- Auth service owns these tables. Logical relations only; no foreign keys.
-- UTC epoch milliseconds: exact Go/HTTP expiry comparison contract.
-- Supported dates 2020-01-01 to 2100-01-01; Shanghai is UTC+08 in this range.
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)),
    created_at_ms INTEGER NOT NULL CHECK(created_at_ms BETWEEN 1577836800000 AND 4102444800000)
);
CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY CHECK(length(token_hash)=64),
    user_id INTEGER NOT NULL,
    created_at_ms INTEGER NOT NULL CHECK(created_at_ms BETWEEN 1577836800000 AND 4102444800000),
    expires_at_ms INTEGER NOT NULL CHECK(expires_at_ms>created_at_ms AND expires_at_ms<=4102444800000)
);
CREATE INDEX IF NOT EXISTS sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at_ms);
CREATE VIEW IF NOT EXISTS users_operations AS SELECT id,username,disabled,
    strftime('%Y-%m-%dT%H:%M:%f',created_at_ms/1000.0,'unixepoch','+8 hours')||'+08:00' AS created_at_shanghai_iso,
    strftime('%Y-%m-%dT%H:%M:%fZ',created_at_ms/1000.0,'unixepoch') AS created_at_utc_iso,
    created_at_ms FROM users;
CREATE VIEW IF NOT EXISTS sessions_operations AS SELECT user_id,
    strftime('%Y-%m-%dT%H:%M:%f',created_at_ms/1000.0,'unixepoch','+8 hours')||'+08:00' AS created_at_shanghai_iso,
    strftime('%Y-%m-%dT%H:%M:%fZ',created_at_ms/1000.0,'unixepoch') AS created_at_utc_iso,
    strftime('%Y-%m-%dT%H:%M:%f',expires_at_ms/1000.0,'unixepoch','+8 hours')||'+08:00' AS expires_at_shanghai_iso,
    strftime('%Y-%m-%dT%H:%M:%fZ',expires_at_ms/1000.0,'unixepoch') AS expires_at_utc_iso,
    created_at_ms,expires_at_ms FROM sessions;
