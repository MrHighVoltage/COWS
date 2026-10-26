ALTER TABLE users ADD COLUMN credential_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN email_key TEXT NOT NULL DEFAULT '';

-- Existing duplicate addresses refuse the upgrade atomically. An operator must
-- correct them explicitly; migrations never choose an account or delete users.
-- Go backfills email_key in this transaction using the same Unicode mapping as
-- account writes and lookups. No custom collation is required by offline SQLite.
CREATE UNIQUE INDEX users_email_unique ON users(email_key) WHERE email_key <> '';

-- Earlier races may have admitted old-password sessions after revocation, and
-- CSV corrections may have left links valid for a former mailbox. Their source
-- generation is unknown, so require fresh authentication and credential links.
DELETE FROM sessions;
DELETE FROM password_reset_tokens;
UPDATE email_messages SET status = 'canceled'
WHERE status = 'pending' AND kind IN ('invitation', 'password_reset');
