CREATE TABLE password_reset_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);

CREATE INDEX password_reset_tokens_user_idx ON password_reset_tokens(user_id, expires_at);
CREATE INDEX password_reset_tokens_expiry_idx ON password_reset_tokens(expires_at, used_at);
