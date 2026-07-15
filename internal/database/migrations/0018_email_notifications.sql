CREATE TABLE email_notifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    recipient TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('timeout_stop', 'timeout_delete')),
    deadline INTEGER NOT NULL,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'canceled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    sent_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (workspace_id, kind)
);

CREATE INDEX email_notifications_pending_idx
    ON email_notifications(status, next_attempt_at);
