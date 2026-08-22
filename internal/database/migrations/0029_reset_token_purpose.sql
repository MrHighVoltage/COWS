ALTER TABLE password_reset_tokens
    ADD COLUMN purpose TEXT NOT NULL DEFAULT 'reset'
    CHECK (purpose IN ('reset', 'invitation'));

DROP INDEX password_reset_tokens_user_idx;
CREATE INDEX password_reset_tokens_user_idx
    ON password_reset_tokens(user_id, purpose, expires_at);
