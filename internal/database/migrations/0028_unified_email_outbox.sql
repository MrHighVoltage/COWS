CREATE TABLE email_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    user_id TEXT,
    recipient TEXT NOT NULL,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    dedupe_key TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'canceled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    sent_at INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX email_messages_dedupe_idx
    ON email_messages(dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX email_messages_pending_idx
    ON email_messages(status, next_attempt_at);
CREATE INDEX email_messages_user_idx
    ON email_messages(user_id, kind, created_at);

INSERT INTO email_messages
    (kind, user_id, recipient, subject, body, dedupe_key, status,
     attempts, next_attempt_at, last_error_code, created_at, sent_at)
SELECT kind, owner_user_id, recipient, subject, body,
       'workspace:' || workspace_id || ':' || kind, status,
       attempts, next_attempt_at, last_error_code, created_at, sent_at
FROM email_notifications;

INSERT INTO email_messages
    (kind, user_id, recipient, subject, body, dedupe_key, status,
     attempts, next_attempt_at, last_error_code, created_at, sent_at)
SELECT 'password_reset', user_id, recipient, subject, body, NULL, status,
       attempts, next_attempt_at, last_error_code, created_at,
       COALESCE(sent_at, 0)
FROM password_reset_emails;

DROP TABLE email_notifications;
DROP TABLE password_reset_emails;
