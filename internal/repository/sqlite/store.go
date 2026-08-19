package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

func (s *Store) CountActiveAdministrators(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0", domain.RoleAdministrator).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active administrators: %w", err)
	}
	return count, nil
}

func (s *Store) FindUserByUsername(ctx context.Context, username string) (repository.UserRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at
		FROM users WHERE username = ?`, username)
	return scanUserRecord(row)
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (repository.UserRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at
		FROM users WHERE lower(email) = lower(?)`, email)
	return scanUserRecord(row)
}

func (s *Store) FindUserByID(ctx context.Context, id string) (domain.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at
		FROM users WHERE id = ?`, id)
	record, err := scanUserRecord(row)
	if err != nil {
		return domain.User{}, err
	}
	return record.User, nil
}

func (s *Store) FindUserCredentialsByID(ctx context.Context, id string) (repository.UserRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at
		FROM users WHERE id = ?`, id)
	return scanUserRecord(row)
}

func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]domain.User, 0)
	for rows.Next() {
		record, err := scanUserRecord(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, record.User)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

func (s *Store) CreateUser(ctx context.Context, user domain.User, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users
		(id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, user.ID, user.Username, user.Email, user.DisplayName, passwordHash,
		user.Role, boolInt(user.Disabled), boolInt(user.MustChangePassword), user.CreatedAt.Unix(), user.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *Store) ImportUsers(ctx context.Context, entries []repository.UserImportEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin user import: %w", err)
	}
	defer tx.Rollback()
	for _, entry := range entries {
		user := entry.User
		if !entry.Existing {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users
				(id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, user.ID, user.Username, user.Email, user.DisplayName,
				entry.PasswordHash, user.Role, boolInt(user.Disabled), boolInt(user.MustChangePassword),
				user.CreatedAt.Unix(), user.UpdatedAt.Unix()); err != nil {
				return fmt.Errorf("create imported user: %w", err)
			}
		} else {
			result, err := tx.ExecContext(ctx, `UPDATE users SET email = CASE WHEN ? <> '' THEN ? ELSE email END, updated_at = ? WHERE id = ?`,
				user.Email, user.Email, user.UpdatedAt.Unix(), user.ID)
			if err != nil {
				return fmt.Errorf("update imported user: %w", err)
			}
			count, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("check imported user update: %w", err)
			}
			if count == 0 {
				return repository.ErrNotFound
			}
		}
		for _, groupID := range entry.GroupIDs {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_groups (user_id, group_id, created_at) VALUES (?, ?, ?)`, user.ID, groupID, user.UpdatedAt.Unix()); err != nil {
				return fmt.Errorf("assign imported user group: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user import: %w", err)
	}
	return nil
}

func (s *Store) RegisterUser(ctx context.Context, user domain.User, passwordHash string, groupIDs []string, userQuota domain.UserQuota) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin user registration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users
		(id, username, email, display_name, password_hash, role, disabled, must_change_password, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, user.ID, user.Username, user.Email, user.DisplayName, passwordHash,
		user.Role, boolInt(user.Disabled), boolInt(user.MustChangePassword), user.CreatedAt.Unix(), user.UpdatedAt.Unix()); err != nil {
		return fmt.Errorf("create registered user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_quotas
		(user_id, max_cpu_millis, max_memory_bytes, max_storage_bytes, max_workspaces, max_running_workspaces, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, user.ID, userQuota.MaxCPUMillis, userQuota.MaxMemoryBytes,
		userQuota.MaxStorageBytes, userQuota.MaxWorkspaces, userQuota.MaxRunningWorkspaces,
		unixOrZero(userQuota.CreatedAt), unixOrZero(userQuota.UpdatedAt)); err != nil {
		return fmt.Errorf("assign registered user quota: %w", err)
	}
	for _, groupID := range groupIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_groups (user_id, group_id, created_at) VALUES (?, ?, ?)", user.ID, groupID, user.CreatedAt.Unix()); err != nil {
			return fmt.Errorf("assign registered user group: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user registration: %w", err)
	}
	return nil
}

func (s *Store) UpdateUserPassword(ctx context.Context, id, passwordHash string, mustChangePassword bool) error {
	result, err := s.db.ExecContext(ctx, "UPDATE users SET password_hash = ?, must_change_password = ?, updated_at = ? WHERE id = ?", passwordHash, boolInt(mustChangePassword), time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check user password update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) ResetPasswordUsingToken(ctx context.Context, tokenHash, passwordHash string, now time.Time) (domain.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin password reset: %w", err)
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT u.id, u.username, u.email, u.display_name, u.password_hash, u.role,
		u.disabled, u.must_change_password, u.created_at, u.updated_at
		FROM password_reset_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ? AND t.used_at IS NULL AND t.expires_at > ?`, tokenHash, now.Unix())
	record, err := scanUserRecord(row)
	if err != nil {
		return domain.User{}, err
	}
	if record.User.Disabled {
		return domain.User{}, repository.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ?, must_change_password = 0, updated_at = ? WHERE id = ?", passwordHash, now.Unix(), record.User.ID); err != nil {
		return domain.User{}, fmt.Errorf("update reset password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", record.User.ID); err != nil {
		return domain.User{}, fmt.Errorf("invalidate reset sessions: %w", err)
	}
	result, err := tx.ExecContext(ctx, "UPDATE password_reset_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL", now.Unix(), tokenHash)
	if err != nil {
		return domain.User{}, fmt.Errorf("consume password reset token: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return domain.User{}, fmt.Errorf("check password reset token: %w", err)
	} else if count == 0 {
		return domain.User{}, repository.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return domain.User{}, fmt.Errorf("commit password reset: %w", err)
	}
	record.User.MustChangePassword = false
	record.User.UpdatedAt = now
	return record.User, nil
}

func (s *Store) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin user status update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?", boolInt(disabled), time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("set user disabled: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check disabled user update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id); err != nil {
			return fmt.Errorf("invalidate disabled user sessions: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user status update: %w", err)
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check user deletion: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) ListUserGroupIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT group_id FROM user_groups WHERE user_id = ? ORDER BY group_id", userID)
	if err != nil {
		return nil, fmt.Errorf("list user groups: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user group: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user groups: %w", err)
	}
	return ids, nil
}

func (s *Store) SetUserGroups(ctx context.Context, userID string, groupIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin user groups update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_groups WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("clear user groups: %w", err)
	}
	for _, groupID := range groupIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_groups (user_id, group_id, created_at) VALUES (?, ?, ?)", userID, groupID, time.Now().UTC().Unix()); err != nil {
			return fmt.Errorf("assign user group: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user groups update: %w", err)
	}
	return nil
}

func (s *Store) ListGroups(ctx context.Context) ([]domain.Group, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, description, created_at, updated_at FROM groups ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	groups := make([]domain.Group, 0)
	for rows.Next() {
		var group domain.Group
		var created, updated int64
		if err := rows.Scan(&group.ID, &group.Name, &group.Description, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		group.CreatedAt = time.Unix(created, 0).UTC()
		group.UpdatedAt = time.Unix(updated, 0).UTC()
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate groups: %w", err)
	}
	return groups, nil
}

func (s *Store) FindGroupByID(ctx context.Context, id string) (domain.Group, error) {
	var group domain.Group
	var created, updated int64
	err := s.db.QueryRowContext(ctx, "SELECT id, name, description, created_at, updated_at FROM groups WHERE id = ?", id).Scan(&group.ID, &group.Name, &group.Description, &created, &updated)
	if err == sql.ErrNoRows {
		return domain.Group{}, repository.ErrNotFound
	}
	if err != nil {
		return domain.Group{}, fmt.Errorf("find group: %w", err)
	}
	group.CreatedAt = time.Unix(created, 0).UTC()
	group.UpdatedAt = time.Unix(updated, 0).UTC()
	return group, nil
}

func (s *Store) FindGroupByName(ctx context.Context, name string) (domain.Group, error) {
	var group domain.Group
	var created, updated int64
	err := s.db.QueryRowContext(ctx, "SELECT id, name, description, created_at, updated_at FROM groups WHERE name = ?", name).Scan(&group.ID, &group.Name, &group.Description, &created, &updated)
	if err == sql.ErrNoRows {
		return domain.Group{}, repository.ErrNotFound
	}
	if err != nil {
		return domain.Group{}, fmt.Errorf("find group by name: %w", err)
	}
	group.CreatedAt = time.Unix(created, 0).UTC()
	group.UpdatedAt = time.Unix(updated, 0).UTC()
	return group, nil
}

func (s *Store) CreateGroup(ctx context.Context, group domain.Group) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO groups (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", group.ID, group.Name, group.Description, group.CreatedAt.Unix(), group.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM groups WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check group deletion: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, session domain.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, user_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		session.TokenHash, session.UserID, session.CreatedAt.Unix(), session.ExpiresAt.Unix(), session.LastSeen.Unix())
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) FindSessionUser(ctx context.Context, tokenHash string, nowUnix int64) (domain.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.email, u.display_name, u.password_hash, u.role, u.disabled, u.must_change_password, u.created_at, u.updated_at
		FROM sessions AS s JOIN users AS u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled = 0`, tokenHash, nowUnix)
	record, err := scanUserRecord(row)
	if err != nil {
		return domain.User{}, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", nowUnix, tokenHash); err != nil {
		return domain.User{}, fmt.Errorf("update session activity: %w", err)
	}
	return record.User, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *Store) DeleteSessionsForUser(ctx context.Context, userID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// DeleteSessionsForUserExcept revokes every session for userID except the one
// matching keepTokenHash. It is used after a password change to invalidate
// other sessions (including stolen ones) while keeping the caller logged in.
// A blank keepTokenHash revokes all sessions for the user.
func (s *Store) DeleteSessionsForUserExcept(ctx context.Context, userID, keepTokenHash string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND token_hash != ?", userID, keepTokenHash); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, nowUnix int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", nowUnix); err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

func (s *Store) RecordAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	metadata := event.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audit_events
		(actor_user_id, event_type, target_type, target_id, metadata_json, created_at)
		VALUES (NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?)`, event.ActorUserID, event.EventType,
		event.TargetType, event.TargetID, string(metadataJSON), event.CreatedAt.Unix()); err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

func (s *Store) ListAuditEvents(ctx context.Context, query domain.AuditQuery) ([]domain.AuditRecord, error) {
	limit := query.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	args := []any{}
	where := ""
	if query.EventType != "" {
		where = " WHERE a.event_type = ?"
		args = append(args, query.EventType)
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, COALESCE(a.actor_user_id, ''), a.event_type,
		a.target_type, COALESCE(a.target_id, ''), a.metadata_json, a.created_at,
		COALESCE(u.username, '') FROM audit_events a LEFT JOIN users u ON u.id = a.actor_user_id`+where+` ORDER BY a.created_at DESC, a.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	result := make([]domain.AuditRecord, 0)
	for rows.Next() {
		var record domain.AuditRecord
		var metadataJSON, actorUsername string
		var createdUnix int64
		if err := rows.Scan(&record.ID, &record.ActorUserID, &record.EventType, &record.TargetType, &record.TargetID, &metadataJSON, &createdUnix, &actorUsername); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if err := json.Unmarshal([]byte(metadataJSON), &record.Metadata); err != nil {
			return nil, fmt.Errorf("decode audit metadata: %w", err)
		}
		record.CreatedAt = timeFromUnix(createdUnix)
		record.ActorUsername = actorUsername
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return result, nil
}

func (s *Store) ListTemplates(ctx context.Context) ([]domain.WorkspaceTemplate, error) {
	rows, err := s.db.QueryContext(ctx, templateSelect+" ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()

	templates := make([]domain.WorkspaceTemplate, 0)
	for rows.Next() {
		template, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		templates = append(templates, template)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate templates: %w", err)
	}
	return templates, nil
}

func (s *Store) FindTemplateByID(ctx context.Context, id string) (domain.WorkspaceTemplate, error) {
	return scanTemplate(s.db.QueryRowContext(ctx, templateSelect+" WHERE id = ?", id))
}

func (s *Store) FindTemplateByName(ctx context.Context, name string) (domain.WorkspaceTemplate, error) {
	return scanTemplate(s.db.QueryRowContext(ctx, templateSelect+" WHERE name = ?", name))
}

func (s *Store) CreateTemplate(ctx context.Context, template domain.WorkspaceTemplate) error {
	accessMethods, roles, groupIDs, configuration, err := marshalTemplateLists(template)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO workspace_templates
		(id, name, description, image_reference, image_digest, default_cpu_millis, max_cpu_millis,
		 default_memory_bytes, max_memory_bytes, resources_configurable, default_storage_bytes, initial_connection_timeout_seconds,
		 stopped_retention_seconds, data_retention_seconds, revision, configuration_json, access_methods_json, allowed_roles_json, group_access_mode, allowed_group_ids_json, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, template.ID, template.Name, template.Description,
		template.ImageReference, template.ImageDigest, template.DefaultCPUMillis, template.MaxCPUMillis,
		template.DefaultMemoryBytes, template.MaxMemoryBytes, boolInt(template.ResourcesConfigurable), template.DefaultStorageBytes, template.InitialConnectionTimeoutSeconds,
		template.StoppedRetentionSeconds, template.DataRetentionSeconds, template.Revision, configuration, accessMethods, roles, template.GroupAccessMode, groupIDs, boolInt(template.Enabled), template.CreatedAt.Unix(), template.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("create template: %w", err)
	}
	return nil
}

func (s *Store) UpdateTemplate(ctx context.Context, template domain.WorkspaceTemplate) error {
	accessMethods, roles, groupIDs, configuration, err := marshalTemplateLists(template)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workspace_templates SET
		name = ?, description = ?, image_reference = ?, image_digest = ?, default_cpu_millis = ?, max_cpu_millis = ?,
		default_memory_bytes = ?, max_memory_bytes = ?, resources_configurable = ?, default_storage_bytes = ?, initial_connection_timeout_seconds = ?,
		stopped_retention_seconds = ?, data_retention_seconds = ?, revision = ?, configuration_json = ?, access_methods_json = ?, allowed_roles_json = ?, group_access_mode = ?, allowed_group_ids_json = ?, enabled = ?, updated_at = ? WHERE id = ?`, template.Name, template.Description,
		template.ImageReference, template.ImageDigest, template.DefaultCPUMillis, template.MaxCPUMillis,
		template.DefaultMemoryBytes, template.MaxMemoryBytes, boolInt(template.ResourcesConfigurable), template.DefaultStorageBytes, template.InitialConnectionTimeoutSeconds,
		template.StoppedRetentionSeconds, template.DataRetentionSeconds, template.Revision, configuration, accessMethods, roles, template.GroupAccessMode, groupIDs, boolInt(template.Enabled), template.UpdatedAt.Unix(), template.ID)
	if err != nil {
		return fmt.Errorf("update template: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check template update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) SetTemplateEnabled(ctx context.Context, id string, enabled bool, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, "UPDATE workspace_templates SET enabled = ?, updated_at = ? WHERE id = ?", boolInt(enabled), updatedAt.Unix(), id)
	if err != nil {
		return fmt.Errorf("set template enabled: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check template state update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) ReserveWorkspacePort(ctx context.Context, allocation domain.PortAllocation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_port_allocations
		(workspace_id, service_name, protocol, container_port, port_pool, host_port)
		VALUES (?, ?, ?, ?, ?, ?)`, allocation.WorkspaceID, allocation.ServiceName, allocation.Protocol,
		allocation.ContainerPort, allocation.PortPool, allocation.HostPort)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return repository.ErrConflict
		}
		return fmt.Errorf("reserve workspace port: %w", err)
	}
	return nil
}

func (s *Store) ListWorkspacePortAllocations(ctx context.Context, workspaceID string) ([]domain.PortAllocation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, service_name, protocol, container_port, port_pool, host_port
		FROM workspace_port_allocations WHERE workspace_id = ? ORDER BY service_name`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace port allocations: %w", err)
	}
	defer rows.Close()
	allocations := make([]domain.PortAllocation, 0)
	for rows.Next() {
		var allocation domain.PortAllocation
		if err := rows.Scan(&allocation.WorkspaceID, &allocation.ServiceName, &allocation.Protocol, &allocation.ContainerPort, &allocation.PortPool, &allocation.HostPort); err != nil {
			return nil, fmt.Errorf("scan workspace port allocation: %w", err)
		}
		allocations = append(allocations, allocation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace port allocations: %w", err)
	}
	return allocations, nil
}

func (s *Store) ReleaseWorkspacePorts(ctx context.Context, workspaceID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM workspace_port_allocations WHERE workspace_id = ?", workspaceID); err != nil {
		return fmt.Errorf("release workspace ports: %w", err)
	}
	return nil
}

const templateSelect = `SELECT id, name, description, image_reference, image_digest, default_cpu_millis,
	max_cpu_millis, default_memory_bytes, max_memory_bytes, resources_configurable, default_storage_bytes, initial_connection_timeout_seconds,
	stopped_retention_seconds, data_retention_seconds, revision, configuration_json, access_methods_json, allowed_roles_json, group_access_mode, allowed_group_ids_json, enabled, created_at, updated_at FROM workspace_templates`

func marshalTemplateLists(template domain.WorkspaceTemplate) (string, string, string, string, error) {
	accessMethods, err := json.Marshal(template.AccessMethods)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode template access methods: %w", err)
	}
	roles, err := json.Marshal(template.AllowedRoles)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode template roles: %w", err)
	}
	groupIDs, err := json.Marshal(template.AllowedGroupIDs)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode template groups: %w", err)
	}
	configuration, err := json.Marshal(template.Configuration)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode template configuration: %w", err)
	}
	return string(accessMethods), string(roles), string(groupIDs), string(configuration), nil
}

func scanTemplate(row scanner) (domain.WorkspaceTemplate, error) {
	var (
		template              domain.WorkspaceTemplate
		accessMethods         string
		roles                 string
		groupIDs              string
		configuration         string
		groupMode             string
		resourcesConfigurable int
		enabled               int
		createdUnix           int64
		updatedUnix           int64
	)
	if err := row.Scan(&template.ID, &template.Name, &template.Description, &template.ImageReference, &template.ImageDigest,
		&template.DefaultCPUMillis, &template.MaxCPUMillis, &template.DefaultMemoryBytes, &template.MaxMemoryBytes,
		&resourcesConfigurable, &template.DefaultStorageBytes, &template.InitialConnectionTimeoutSeconds, &template.StoppedRetentionSeconds,
		&template.DataRetentionSeconds, &template.Revision, &configuration, &accessMethods, &roles, &groupMode, &groupIDs, &enabled, &createdUnix, &updatedUnix); err != nil {
		if err == sql.ErrNoRows {
			return domain.WorkspaceTemplate{}, repository.ErrNotFound
		}
		return domain.WorkspaceTemplate{}, fmt.Errorf("scan template: %w", err)
	}
	if err := json.Unmarshal([]byte(accessMethods), &template.AccessMethods); err != nil {
		return domain.WorkspaceTemplate{}, fmt.Errorf("decode template access methods: %w", err)
	}
	// Drop the removed legacy web-app access flag when reading older databases.
	// Existing templates remain usable, but the obsolete capability cannot leak
	// back into the administrator UI or authorize a route.
	filteredMethods := template.AccessMethods[:0]
	for _, method := range template.AccessMethods {
		if method.Valid() {
			filteredMethods = append(filteredMethods, method)
		}
	}
	template.AccessMethods = filteredMethods
	if err := json.Unmarshal([]byte(roles), &template.AllowedRoles); err != nil {
		return domain.WorkspaceTemplate{}, fmt.Errorf("decode template roles: %w", err)
	}
	if groupIDs != "" {
		if err := json.Unmarshal([]byte(groupIDs), &template.AllowedGroupIDs); err != nil {
			return domain.WorkspaceTemplate{}, fmt.Errorf("decode template groups: %w", err)
		}
	}
	template.GroupAccessMode = groupMode
	template.ResourcesConfigurable = resourcesConfigurable != 0
	if template.GroupAccessMode == "" {
		template.GroupAccessMode = "exclude"
	}
	if configuration != "" && configuration != "{}" {
		if err := json.Unmarshal([]byte(configuration), &template.Configuration); err != nil {
			return domain.WorkspaceTemplate{}, fmt.Errorf("decode template configuration: %w", err)
		}
	}
	if template.Revision <= 0 {
		template.Revision = 1
	}
	template.Enabled = enabled != 0
	template.CreatedAt = time.Unix(createdUnix, 0).UTC()
	template.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return template, nil
}

func (s *Store) ListWorkspacesForUser(ctx context.Context, ownerUserID string) ([]domain.Workspace, error) {
	return s.listWorkspaces(ctx, workspaceSelect+" WHERE owner_user_id = ? ORDER BY name", ownerUserID)
}

func (s *Store) ListAllWorkspaces(ctx context.Context) ([]domain.Workspace, error) {
	return s.listWorkspaces(ctx, workspaceSelect+" ORDER BY owner_user_id, name")
}

func (s *Store) FindWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error) {
	return scanWorkspace(s.db.QueryRowContext(ctx, workspaceSelect+" WHERE id = ?", id))
}

func (s *Store) FindWorkspaceByOwnerAndName(ctx context.Context, ownerUserID, name string) (domain.Workspace, error) {
	return scanWorkspace(s.db.QueryRowContext(ctx, workspaceSelect+" WHERE owner_user_id = ? AND name = ?", ownerUserID, name))
}

func (s *Store) CreateWorkspace(ctx context.Context, workspace domain.Workspace) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces
		(id, owner_user_id, template_id, template_revision, template_configuration_json, template_secrets_json, template_image_reference, template_image_digest, name, desired_state, observed_state, runtime_id, observed_error_code, observed_error,
		 allocated_cpu_millis, allocated_memory_bytes, allocated_storage_bytes, initial_connection_timeout_seconds,
		 stopped_retention_seconds, data_retention_seconds, created_at, updated_at, observed_at, started_at,
		 last_connected_at, active_sessions, idle_since, stopped_at, container_deleted_at, data_archive_eligible_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, workspace.ID, workspace.OwnerUserID, workspace.TemplateID,
		workspace.TemplateRevision, templateConfigurationJSON(workspace.TemplateConfiguration), templateSecretsJSON(workspace.TemplateSecrets), workspace.TemplateImageReference, workspace.TemplateImageDigest, workspace.Name, workspace.DesiredState, workspace.ObservedState, workspace.RuntimeID, workspace.ObservedErrorCode, workspace.ObservedError,
		workspace.AllocatedCPUMillis, workspace.AllocatedMemoryBytes, workspace.AllocatedStorageBytes,
		workspace.InitialConnectionTimeoutSeconds, workspace.StoppedRetentionSeconds, workspace.DataRetentionSeconds,
		unixOrZero(workspace.CreatedAt), unixOrZero(workspace.UpdatedAt), unixOrZero(workspace.ObservedAt), unixOrZero(workspace.StartedAt),
		unixOrZero(workspace.LastConnectedAt), workspace.ActiveSessions, unixOrZero(workspace.IdleSince), unixOrZero(workspace.StoppedAt), unixOrZero(workspace.ContainerDeletedAt), unixOrZero(workspace.DataArchiveEligibleAt))
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	return nil
}

func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace deletion: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteWorkspaceRetainingStorage(ctx context.Context, id string, volumes []domain.RetainedWorkspaceVolume, directory *domain.RetainedWorkspaceDirectory) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin retained storage transaction: %w", err)
	}
	defer tx.Rollback()
	for _, volume := range volumes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO retained_workspace_volumes
			(volume_name, workspace_id, owner_user_id, template_id, mount_name, container_path, read_only, retained_at, workspace_name, template_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, volume.VolumeName, volume.WorkspaceID, volume.OwnerUserID,
			volume.TemplateID, volume.MountName, volume.ContainerPath, boolInt(volume.ReadOnly), unixOrZero(volume.RetainedAt),
			volume.WorkspaceName, volume.TemplateName); err != nil {
			return fmt.Errorf("retain workspace volume metadata: %w", err)
		}
	}
	if directory != nil {
		mountsJSON, err := json.Marshal(directory.Mounts)
		if err != nil {
			return fmt.Errorf("encode retained directory mounts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO retained_workspace_directories
			(workspace_id, owner_user_id, template_id, template_name, workspace_name, archive_path, mounts_json, retained_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, directory.WorkspaceID, directory.OwnerUserID, directory.TemplateID, directory.TemplateName,
			directory.WorkspaceName, directory.ArchivePath, string(mountsJSON), unixOrZero(directory.RetainedAt)); err != nil {
			return fmt.Errorf("retain workspace directory metadata: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM workspaces WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete workspace with retained storage: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace deletion with retained storage: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit retained storage transaction: %w", err)
	}
	return nil
}

func (s *Store) ListRetainedWorkspaceVolumes(ctx context.Context, workspaceID string) ([]domain.RetainedWorkspaceVolume, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT volume_name, workspace_id, owner_user_id, template_id,
		mount_name, container_path, read_only, retained_at, workspace_name, template_name
		FROM retained_workspace_volumes WHERE workspace_id = ? ORDER BY mount_name`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list retained workspace volumes: %w", err)
	}
	defer rows.Close()
	volumes, err := scanRetainedWorkspaceVolumes(rows)
	if err != nil {
		return nil, err
	}
	return volumes, nil
}

func (s *Store) ListAllRetainedWorkspaceVolumes(ctx context.Context) ([]domain.RetainedWorkspaceVolume, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT volume_name, workspace_id, owner_user_id, template_id,
		mount_name, container_path, read_only, retained_at, workspace_name, template_name
		FROM retained_workspace_volumes ORDER BY retained_at DESC, volume_name`)
	if err != nil {
		return nil, fmt.Errorf("list all retained workspace volumes: %w", err)
	}
	defer rows.Close()
	volumes, err := scanRetainedWorkspaceVolumes(rows)
	if err != nil {
		return nil, err
	}
	return volumes, nil
}

func (s *Store) ListRetainedWorkspaceVolumesForOwner(ctx context.Context, ownerUserID string) ([]domain.RetainedWorkspaceVolume, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT volume_name, workspace_id, owner_user_id, template_id,
		mount_name, container_path, read_only, retained_at, workspace_name, template_name
		FROM retained_workspace_volumes WHERE owner_user_id = ? ORDER BY retained_at DESC, volume_name`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list retained workspace volumes for owner: %w", err)
	}
	defer rows.Close()
	volumes, err := scanRetainedWorkspaceVolumes(rows)
	if err != nil {
		return nil, err
	}
	return volumes, nil
}

func scanRetainedWorkspaceVolumes(rows *sql.Rows) ([]domain.RetainedWorkspaceVolume, error) {
	volumes := make([]domain.RetainedWorkspaceVolume, 0)
	for rows.Next() {
		var volume domain.RetainedWorkspaceVolume
		var readOnly int
		var retainedUnix int64
		if err := rows.Scan(&volume.VolumeName, &volume.WorkspaceID, &volume.OwnerUserID, &volume.TemplateID,
			&volume.MountName, &volume.ContainerPath, &readOnly, &retainedUnix, &volume.WorkspaceName, &volume.TemplateName); err != nil {
			return nil, fmt.Errorf("scan retained workspace volume: %w", err)
		}
		volume.ReadOnly = readOnly != 0
		volume.RetainedAt = timeFromUnix(retainedUnix)
		volumes = append(volumes, volume)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retained workspace volumes: %w", err)
	}
	return volumes, nil
}

func (s *Store) DeleteRetainedWorkspaceVolume(ctx context.Context, volumeName string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM retained_workspace_volumes WHERE volume_name = ?", volumeName)
	if err != nil {
		return fmt.Errorf("delete retained volume metadata: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check retained volume metadata deletion: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// FindRetainedWorkspaceVolume is the read-only counterpart of
// ConsumeRetainedWorkspaceVolume, used for self-service download where the
// tombstone must survive the request.
func (s *Store) FindRetainedWorkspaceVolume(ctx context.Context, workspaceID, mountName, ownerUserID string) (domain.RetainedWorkspaceVolume, error) {
	row := s.db.QueryRowContext(ctx, `SELECT volume_name, workspace_id, owner_user_id, template_id,
		mount_name, container_path, read_only, retained_at, workspace_name, template_name
		FROM retained_workspace_volumes WHERE workspace_id = ? AND mount_name = ? AND owner_user_id = ?`, workspaceID, mountName, ownerUserID)
	var volume domain.RetainedWorkspaceVolume
	var readOnly int
	var retainedUnix int64
	if err := row.Scan(&volume.VolumeName, &volume.WorkspaceID, &volume.OwnerUserID, &volume.TemplateID,
		&volume.MountName, &volume.ContainerPath, &readOnly, &retainedUnix, &volume.WorkspaceName, &volume.TemplateName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RetainedWorkspaceVolume{}, repository.ErrNotFound
		}
		return domain.RetainedWorkspaceVolume{}, fmt.Errorf("find retained workspace volume: %w", err)
	}
	volume.ReadOnly = readOnly != 0
	volume.RetainedAt = timeFromUnix(retainedUnix)
	return volume, nil
}

// ConsumeRetainedWorkspaceVolume looks the row up scoped to ownerUserID
// (so a mismatched owner is indistinguishable from a missing row) and, only
// if found, deletes it in the same call. It does not use an explicit
// transaction: SQLite's single-writer model already serializes the SELECT
// and DELETE against any concurrent consumer, and the RowsAffected check
// below detects the case where a second caller won the race between them.
func (s *Store) ConsumeRetainedWorkspaceVolume(ctx context.Context, workspaceID, mountName, ownerUserID string) (domain.RetainedWorkspaceVolume, error) {
	volume, err := s.FindRetainedWorkspaceVolume(ctx, workspaceID, mountName, ownerUserID)
	if err != nil {
		return domain.RetainedWorkspaceVolume{}, err
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM retained_workspace_volumes WHERE volume_name = ? AND owner_user_id = ?", volume.VolumeName, ownerUserID)
	if err != nil {
		return domain.RetainedWorkspaceVolume{}, fmt.Errorf("consume retained workspace volume: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return domain.RetainedWorkspaceVolume{}, fmt.Errorf("check retained workspace volume consumption: %w", err)
	} else if count == 0 {
		return domain.RetainedWorkspaceVolume{}, repository.ErrConflict
	}
	return volume, nil
}

func (s *Store) ListAllRetainedWorkspaceDirectories(ctx context.Context) ([]domain.RetainedWorkspaceDirectory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, owner_user_id, template_id, template_name, workspace_name,
		archive_path, mounts_json, retained_at FROM retained_workspace_directories ORDER BY retained_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all retained workspace directories: %w", err)
	}
	defer rows.Close()
	directories := make([]domain.RetainedWorkspaceDirectory, 0)
	for rows.Next() {
		directory, err := scanRetainedWorkspaceDirectory(rows)
		if err != nil {
			return nil, err
		}
		directories = append(directories, directory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all retained workspace directories: %w", err)
	}
	return directories, nil
}

func (s *Store) ListRetainedWorkspaceDirectoriesForOwner(ctx context.Context, ownerUserID string) ([]domain.RetainedWorkspaceDirectory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, owner_user_id, template_id, template_name, workspace_name,
		archive_path, mounts_json, retained_at FROM retained_workspace_directories WHERE owner_user_id = ? ORDER BY retained_at DESC`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list retained workspace directories for owner: %w", err)
	}
	defer rows.Close()
	directories := make([]domain.RetainedWorkspaceDirectory, 0)
	for rows.Next() {
		directory, err := scanRetainedWorkspaceDirectory(rows)
		if err != nil {
			return nil, err
		}
		directories = append(directories, directory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retained workspace directories: %w", err)
	}
	return directories, nil
}

func (s *Store) DeleteRetainedWorkspaceDirectory(ctx context.Context, workspaceID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM retained_workspace_directories WHERE workspace_id = ?", workspaceID)
	if err != nil {
		return fmt.Errorf("delete retained directory metadata: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check retained directory metadata deletion: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// FindRetainedWorkspaceDirectory is the read-only counterpart of
// ConsumeRetainedWorkspaceDirectory, used for self-service download.
func (s *Store) FindRetainedWorkspaceDirectory(ctx context.Context, workspaceID, ownerUserID string) (domain.RetainedWorkspaceDirectory, error) {
	row := s.db.QueryRowContext(ctx, `SELECT workspace_id, owner_user_id, template_id, template_name, workspace_name,
		archive_path, mounts_json, retained_at FROM retained_workspace_directories WHERE workspace_id = ? AND owner_user_id = ?`, workspaceID, ownerUserID)
	directory, err := scanRetainedWorkspaceDirectory(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RetainedWorkspaceDirectory{}, repository.ErrNotFound
		}
		return domain.RetainedWorkspaceDirectory{}, err
	}
	return directory, nil
}

// FindRetainedWorkspaceDirectoryByID is the administrator-recovery
// counterpart of FindRetainedWorkspaceDirectory: no owner_user_id filter,
// since an administrator must be able to reach a tombstone whose owning
// user account has since been deleted.
func (s *Store) FindRetainedWorkspaceDirectoryByID(ctx context.Context, workspaceID string) (domain.RetainedWorkspaceDirectory, error) {
	row := s.db.QueryRowContext(ctx, `SELECT workspace_id, owner_user_id, template_id, template_name, workspace_name,
		archive_path, mounts_json, retained_at FROM retained_workspace_directories WHERE workspace_id = ?`, workspaceID)
	directory, err := scanRetainedWorkspaceDirectory(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RetainedWorkspaceDirectory{}, repository.ErrNotFound
		}
		return domain.RetainedWorkspaceDirectory{}, err
	}
	return directory, nil
}

// ConsumeRetainedWorkspaceDirectory is the directory equivalent of
// ConsumeRetainedWorkspaceVolume; see its comment for the concurrency model.
func (s *Store) ConsumeRetainedWorkspaceDirectory(ctx context.Context, workspaceID, ownerUserID string) (domain.RetainedWorkspaceDirectory, error) {
	directory, err := s.FindRetainedWorkspaceDirectory(ctx, workspaceID, ownerUserID)
	if err != nil {
		return domain.RetainedWorkspaceDirectory{}, err
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM retained_workspace_directories WHERE workspace_id = ? AND owner_user_id = ?", workspaceID, ownerUserID)
	if err != nil {
		return domain.RetainedWorkspaceDirectory{}, fmt.Errorf("consume retained workspace directory: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return domain.RetainedWorkspaceDirectory{}, fmt.Errorf("check retained workspace directory consumption: %w", err)
	} else if count == 0 {
		return domain.RetainedWorkspaceDirectory{}, repository.ErrConflict
	}
	return directory, nil
}

func scanRetainedWorkspaceDirectory(row scanner) (domain.RetainedWorkspaceDirectory, error) {
	var directory domain.RetainedWorkspaceDirectory
	var mountsJSON string
	var retainedUnix int64
	if err := row.Scan(&directory.WorkspaceID, &directory.OwnerUserID, &directory.TemplateID, &directory.TemplateName, &directory.WorkspaceName,
		&directory.ArchivePath, &mountsJSON, &retainedUnix); err != nil {
		return domain.RetainedWorkspaceDirectory{}, fmt.Errorf("scan retained workspace directory: %w", err)
	}
	if err := json.Unmarshal([]byte(mountsJSON), &directory.Mounts); err != nil {
		return domain.RetainedWorkspaceDirectory{}, fmt.Errorf("decode retained directory mounts: %w", err)
	}
	directory.RetainedAt = timeFromUnix(retainedUnix)
	return directory, nil
}

func (s *Store) CreatePasswordResetToken(ctx context.Context, token domain.PasswordResetToken) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password reset token: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM password_reset_tokens WHERE user_id = ?", token.UserID); err != nil {
		return fmt.Errorf("replace password reset token: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE password_reset_emails SET status = 'canceled' WHERE user_id = ? AND status = 'pending'", token.UserID); err != nil {
		return fmt.Errorf("cancel previous password reset emails: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO password_reset_tokens (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)", token.TokenHash, token.UserID, token.ExpiresAt.Unix(), token.CreatedAt.Unix()); err != nil {
		return fmt.Errorf("store password reset token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit password reset token: %w", err)
	}
	return nil
}

func (s *Store) UpsertPasswordResetEmail(ctx context.Context, email domain.PasswordResetEmail) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO password_reset_emails
		(user_id, recipient, subject, body, status, attempts, next_attempt_at, last_error_code, created_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, email.UserID, email.Recipient, email.Subject, email.Body, email.Status,
		email.Attempts, email.NextAttemptAt.Unix(), email.LastErrorCode, email.CreatedAt.Unix(), unixOrZero(email.SentAt))
	if err != nil {
		return fmt.Errorf("store password reset email: %w", err)
	}
	return nil
}

func (s *Store) ListPendingPasswordResetEmails(ctx context.Context, now time.Time, limit int) ([]domain.PasswordResetEmail, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, recipient, subject, body, status, attempts,
		next_attempt_at, last_error_code, created_at, COALESCE(sent_at, 0)
		FROM password_reset_emails WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending password reset emails: %w", err)
	}
	defer rows.Close()
	result := make([]domain.PasswordResetEmail, 0)
	for rows.Next() {
		var email domain.PasswordResetEmail
		var nextAttempt, created, sent int64
		if err := rows.Scan(&email.ID, &email.UserID, &email.Recipient, &email.Subject, &email.Body, &email.Status, &email.Attempts, &nextAttempt, &email.LastErrorCode, &created, &sent); err != nil {
			return nil, fmt.Errorf("scan password reset email: %w", err)
		}
		email.NextAttemptAt, email.CreatedAt, email.SentAt = timeFromUnix(nextAttempt), timeFromUnix(created), timeFromUnix(sent)
		result = append(result, email)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate password reset emails: %w", err)
	}
	return result, nil
}

func (s *Store) MarkPasswordResetEmailSent(ctx context.Context, id int64, sentAt time.Time) error {
	result, err := s.db.ExecContext(ctx, "UPDATE password_reset_emails SET status = 'sent', sent_at = ? WHERE id = ? AND status = 'pending'", sentAt.Unix(), id)
	if err != nil {
		return fmt.Errorf("mark password reset email sent: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check password reset email sent: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) MarkPasswordResetEmailFailed(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, errorCode string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE password_reset_emails SET attempts = ?, next_attempt_at = ?, last_error_code = ? WHERE id = ? AND status = 'pending'", attempts, nextAttemptAt.Unix(), errorCode, id)
	if err != nil {
		return fmt.Errorf("mark password reset email failed: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check password reset email failure: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) MarkPasswordResetEmailCanceled(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "UPDATE password_reset_emails SET status = 'canceled' WHERE id = ? AND status = 'pending'", id)
	if err != nil {
		return fmt.Errorf("cancel password reset email: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check password reset email cancellation: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) SetWorkspaceDesiredState(ctx context.Context, id string, state domain.DesiredWorkspaceState, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, "UPDATE workspaces SET desired_state = ?, updated_at = ? WHERE id = ?", state, updatedAt.Unix(), id)
	if err != nil {
		return fmt.Errorf("set workspace desired state: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace desired state update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) UpdateWorkspaceObservedState(ctx context.Context, id, observedState, runtimeID, observedErrorCode, observedError string, observedAt, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET observed_state = ?, runtime_id = ?, observed_error_code = ?, observed_error = ?, observed_at = ?, updated_at = ? WHERE id = ?`, observedState, runtimeID, observedErrorCode, observedError, observedAt.Unix(), updatedAt.Unix(), id)
	if err != nil {
		return fmt.Errorf("update workspace observed state: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace observed state update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) UpdateWorkspaceLifecycle(ctx context.Context, id string, startedAt, lastConnectedAt, stoppedAt, containerDeletedAt, dataArchiveEligibleAt, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET started_at = ?, last_connected_at = ?, stopped_at = ?,
		container_deleted_at = ?, data_archive_eligible_at = ?, updated_at = ? WHERE id = ?`, unixOrZero(startedAt),
		unixOrZero(lastConnectedAt), unixOrZero(stoppedAt), unixOrZero(containerDeletedAt), unixOrZero(dataArchiveEligibleAt), unixOrZero(updatedAt), id)
	if err != nil {
		return fmt.Errorf("update workspace lifecycle: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace lifecycle update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// RecordWorkspaceSessionStart increments active_sessions and clears
// idle_since (a workspace with any open session is never idle) as one
// atomic statement, so concurrent connects/disconnects can't race each
// other into an inconsistent count.
func (s *Store) RecordWorkspaceSessionStart(ctx context.Context, id string, connectedAt, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET active_sessions = active_sessions + 1,
		idle_since = 0, last_connected_at = ?, updated_at = ? WHERE id = ?`,
		unixOrZero(connectedAt), unixOrZero(updatedAt), id)
	if err != nil {
		return fmt.Errorf("record workspace session start: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace session start: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// RecordWorkspaceSessionEnd decrements active_sessions (never below zero)
// and, only if that was the last open session, sets idle_since to idleAt so
// EvaluateTimeouts starts measuring the idle-shutdown deadline from the
// moment of this disconnect rather than from the workspace's original
// start. The CASE condition reads active_sessions' pre-update value (SQLite
// evaluates every SET expression against the row as it was before the
// statement, not sequentially), so this is race-safe without a
// read-modify-write round trip.
func (s *Store) RecordWorkspaceSessionEnd(ctx context.Context, id string, idleAt, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET
		active_sessions = MAX(active_sessions - 1, 0),
		idle_since = CASE WHEN active_sessions <= 1 THEN ? ELSE idle_since END,
		updated_at = ?
		WHERE id = ?`, unixOrZero(idleAt), unixOrZero(updatedAt), id)
	if err != nil {
		return fmt.Errorf("record workspace session end: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace session end: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// ResetWorkspaceSessions unconditionally zeroes active_sessions and sets
// idle_since to idleAt, regardless of the row's current count. Used on
// every fresh start so a stale count left over from a prior run (e.g. a
// disconnect hook that never fired because the process restarted) cannot
// linger - unlike RecordWorkspaceSessionEnd, which only ever decrements by
// one and so cannot correct a count that's already wrong by more than one.
func (s *Store) ResetWorkspaceSessions(ctx context.Context, id string, idleAt, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET active_sessions = 0, idle_since = ?, updated_at = ? WHERE id = ?`,
		unixOrZero(idleAt), unixOrZero(updatedAt), id)
	if err != nil {
		return fmt.Errorf("reset workspace sessions: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace sessions reset: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) UpdateWorkspaceOperation(ctx context.Context, id, operation, status, operationError string, startedAt, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET operation = ?, operation_status = ?, operation_error = ?,
		operation_started_at = ?, operation_updated_at = ?, updated_at = ? WHERE id = ?`, operation, status, operationError,
		unixOrZero(startedAt), unixOrZero(updatedAt), unixOrZero(updatedAt), id)
	if err != nil {
		return fmt.Errorf("update workspace operation: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check workspace operation update: %w", err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) WorkspaceAllocations(ctx context.Context, ownerUserID string) (domain.AllocationSummary, error) {
	return s.workspaceAllocations(ctx, " WHERE owner_user_id = ?", ownerUserID)
}

func (s *Store) AllWorkspaceAllocations(ctx context.Context) (domain.AllocationSummary, error) {
	return s.workspaceAllocations(ctx, "")
}

func (s *Store) workspaceAllocations(ctx context.Context, condition string, args ...any) (domain.AllocationSummary, error) {
	var summary domain.AllocationSummary
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN observed_state = 'running' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN observed_state = 'running' THEN allocated_cpu_millis ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN observed_state = 'running' THEN allocated_memory_bytes ELSE 0 END), 0), 0
		FROM workspaces`+condition, args...).Scan(&summary.WorkspaceCount, &summary.RunningWorkspaceCount, &summary.Resources.CPUMillis,
		&summary.Resources.MemoryBytes, &summary.Resources.StorageBytes)
	if err != nil {
		return domain.AllocationSummary{}, fmt.Errorf("sum workspace allocations: %w", err)
	}
	return summary, nil
}

func (s *Store) FindUserQuota(ctx context.Context, userID string) (domain.UserQuota, error) {
	return scanUserQuota(s.db.QueryRowContext(ctx, `SELECT user_id, max_cpu_millis, max_memory_bytes,
		max_storage_bytes, max_workspaces, max_running_workspaces, created_at, updated_at FROM user_quotas WHERE user_id = ?`, userID))
}

func (s *Store) ListUserQuotas(ctx context.Context) ([]domain.UserQuota, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, max_cpu_millis, max_memory_bytes,
		max_storage_bytes, max_workspaces, max_running_workspaces, created_at, updated_at FROM user_quotas ORDER BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("list user quotas: %w", err)
	}
	defer rows.Close()
	quotas := make([]domain.UserQuota, 0)
	for rows.Next() {
		quota, err := scanUserQuota(rows)
		if err != nil {
			return nil, err
		}
		quotas = append(quotas, quota)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user quotas: %w", err)
	}
	return quotas, nil
}

func (s *Store) UpsertUserQuota(ctx context.Context, quota domain.UserQuota) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_quotas
		(user_id, max_cpu_millis, max_memory_bytes, max_storage_bytes, max_workspaces, max_running_workspaces, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET max_cpu_millis = excluded.max_cpu_millis,
		max_memory_bytes = excluded.max_memory_bytes, max_storage_bytes = excluded.max_storage_bytes,
		max_workspaces = excluded.max_workspaces, max_running_workspaces = excluded.max_running_workspaces, updated_at = excluded.updated_at`, quota.UserID,
		quota.MaxCPUMillis, quota.MaxMemoryBytes, quota.MaxStorageBytes, quota.MaxWorkspaces, quota.MaxRunningWorkspaces,
		unixOrZero(quota.CreatedAt), unixOrZero(quota.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert user quota: %w", err)
	}
	return nil
}

func (s *Store) DeleteUserQuota(ctx context.Context, userID string) error {
	return deleteQuota(ctx, s.db, "DELETE FROM user_quotas WHERE user_id = ?", userID, "user")
}

func (s *Store) FindGroupQuota(ctx context.Context, groupID string) (domain.GroupQuota, error) {
	return scanGroupQuota(s.db.QueryRowContext(ctx, `SELECT group_id, max_cpu_millis, max_memory_bytes,
		max_storage_bytes, max_workspaces, max_running_workspaces, created_at, updated_at FROM group_quotas WHERE group_id = ?`, groupID))
}

func (s *Store) ListGroupQuotas(ctx context.Context) ([]domain.GroupQuota, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, max_cpu_millis, max_memory_bytes,
		max_storage_bytes, max_workspaces, max_running_workspaces, created_at, updated_at FROM group_quotas ORDER BY group_id`)
	if err != nil {
		return nil, fmt.Errorf("list group quotas: %w", err)
	}
	defer rows.Close()
	quotas := make([]domain.GroupQuota, 0)
	for rows.Next() {
		quota, err := scanGroupQuota(rows)
		if err != nil {
			return nil, err
		}
		quotas = append(quotas, quota)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group quotas: %w", err)
	}
	return quotas, nil
}

func (s *Store) ListGroupQuotasForUser(ctx context.Context, userID string) ([]domain.GroupQuota, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT q.group_id, q.max_cpu_millis, q.max_memory_bytes,
		q.max_storage_bytes, q.max_workspaces, q.max_running_workspaces, q.created_at, q.updated_at
		FROM group_quotas AS q JOIN user_groups AS ug ON ug.group_id = q.group_id
		WHERE ug.user_id = ? ORDER BY q.group_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user group quotas: %w", err)
	}
	defer rows.Close()
	quotas := make([]domain.GroupQuota, 0)
	for rows.Next() {
		quota, err := scanGroupQuota(rows)
		if err != nil {
			return nil, err
		}
		quotas = append(quotas, quota)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user group quotas: %w", err)
	}
	return quotas, nil
}

func (s *Store) UpsertGroupQuota(ctx context.Context, quota domain.GroupQuota) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO group_quotas
		(group_id, max_cpu_millis, max_memory_bytes, max_storage_bytes, max_workspaces, max_running_workspaces, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(group_id) DO UPDATE SET max_cpu_millis = excluded.max_cpu_millis,
		max_memory_bytes = excluded.max_memory_bytes, max_storage_bytes = excluded.max_storage_bytes,
		max_workspaces = excluded.max_workspaces, max_running_workspaces = excluded.max_running_workspaces,
		updated_at = excluded.updated_at`, quota.GroupID, quota.MaxCPUMillis, quota.MaxMemoryBytes,
		quota.MaxStorageBytes, quota.MaxWorkspaces, quota.MaxRunningWorkspaces,
		unixOrZero(quota.CreatedAt), unixOrZero(quota.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert group quota: %w", err)
	}
	return nil
}

func (s *Store) DeleteGroupQuota(ctx context.Context, groupID string) error {
	return deleteQuota(ctx, s.db, "DELETE FROM group_quotas WHERE group_id = ?", groupID, "group")
}

func deleteQuota(ctx context.Context, db *sql.DB, query, id, quotaType string) error {
	result, err := db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete %s quota: %w", quotaType, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check %s quota deletion: %w", quotaType, err)
	}
	if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) FindHostSettings(ctx context.Context) (domain.HostSettings, error) {
	var settings domain.HostSettings
	var createdUnix, updatedUnix int64
	err := s.db.QueryRowContext(ctx, `SELECT id, host_storage_bytes, cpu_overbooking_factor,
		memory_overbooking_factor,
		reserved_storage_bytes, created_at, updated_at
		FROM host_settings WHERE id = 1`).Scan(&settings.ID, &settings.HostStorageBytes,
		&settings.CPUOverbookingFactor, &settings.MemoryOverbookingFactor, &settings.ReservedStorageBytes,
		&createdUnix, &updatedUnix)
	if err == sql.ErrNoRows {
		return domain.HostSettings{}, repository.ErrNotFound
	}
	if err != nil {
		return domain.HostSettings{}, fmt.Errorf("find host settings: %w", err)
	}
	settings.CreatedAt = time.Unix(createdUnix, 0).UTC()
	settings.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return settings, nil
}

func (s *Store) UpsertHostSettings(ctx context.Context, settings domain.HostSettings) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_settings
		(id, host_storage_bytes, reserved_cpu_millis, reserved_memory_bytes, overbooking_factor, cpu_overbooking_factor, memory_overbooking_factor, reserved_storage_bytes, created_at, updated_at)
		VALUES (1, ?, 0, 0, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET host_storage_bytes = excluded.host_storage_bytes,
		overbooking_factor = excluded.overbooking_factor,
		cpu_overbooking_factor = excluded.cpu_overbooking_factor,
		memory_overbooking_factor = excluded.memory_overbooking_factor,
		reserved_storage_bytes = excluded.reserved_storage_bytes,
		updated_at = excluded.updated_at`, settings.HostStorageBytes, settings.CPUOverbookingFactor,
		settings.CPUOverbookingFactor, settings.MemoryOverbookingFactor, settings.ReservedStorageBytes,
		unixOrZero(settings.CreatedAt), unixOrZero(settings.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert host settings: %w", err)
	}
	return nil
}

func (s *Store) UpsertEmailNotification(ctx context.Context, notification domain.EmailNotification) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO email_notifications
		(workspace_id, owner_user_id, recipient, kind, deadline, subject, body, status, attempts, next_attempt_at, last_error_code, created_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, '', ?, 0)
		ON CONFLICT(workspace_id, kind) DO UPDATE SET owner_user_id = excluded.owner_user_id,
		recipient = excluded.recipient, deadline = excluded.deadline, subject = excluded.subject, body = excluded.body,
		status = CASE WHEN email_notifications.deadline != excluded.deadline OR email_notifications.recipient != excluded.recipient THEN 'pending' ELSE email_notifications.status END,
		attempts = CASE WHEN email_notifications.deadline != excluded.deadline OR email_notifications.recipient != excluded.recipient THEN 0 ELSE email_notifications.attempts END,
		next_attempt_at = CASE WHEN email_notifications.deadline != excluded.deadline OR email_notifications.recipient != excluded.recipient THEN excluded.next_attempt_at ELSE email_notifications.next_attempt_at END,
		last_error_code = CASE WHEN email_notifications.deadline != excluded.deadline OR email_notifications.recipient != excluded.recipient THEN '' ELSE email_notifications.last_error_code END,
		sent_at = CASE WHEN email_notifications.deadline != excluded.deadline OR email_notifications.recipient != excluded.recipient THEN 0 ELSE email_notifications.sent_at END`,
		notification.WorkspaceID, notification.OwnerUserID, notification.Recipient, notification.Kind,
		unixOrZero(notification.Deadline), notification.Subject, notification.Body, unixOrZero(notification.NextAttemptAt), unixOrZero(notification.CreatedAt))
	if err != nil {
		return fmt.Errorf("upsert email notification: %w", err)
	}
	return nil
}

func (s *Store) ListPendingEmailNotifications(ctx context.Context, now time.Time, limit int) ([]domain.EmailNotification, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, owner_user_id, recipient, kind, deadline,
		subject, body, status, attempts, next_attempt_at, last_error_code, created_at, sent_at
		FROM email_notifications WHERE status = 'pending' AND next_attempt_at <= ?
		ORDER BY next_attempt_at, id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending email notifications: %w", err)
	}
	defer rows.Close()
	result := make([]domain.EmailNotification, 0)
	for rows.Next() {
		var notification domain.EmailNotification
		var deadlineUnix, nextAttemptUnix, createdUnix, sentUnix int64
		if err := rows.Scan(&notification.ID, &notification.WorkspaceID, &notification.OwnerUserID, &notification.Recipient, &notification.Kind, &deadlineUnix, &notification.Subject, &notification.Body, &notification.Status, &notification.Attempts, &nextAttemptUnix, &notification.LastErrorCode, &createdUnix, &sentUnix); err != nil {
			return nil, fmt.Errorf("scan email notification: %w", err)
		}
		notification.Deadline = timeFromUnix(deadlineUnix)
		notification.NextAttemptAt = timeFromUnix(nextAttemptUnix)
		notification.CreatedAt = timeFromUnix(createdUnix)
		notification.SentAt = timeFromUnix(sentUnix)
		result = append(result, notification)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate email notifications: %w", err)
	}
	return result, nil
}

func (s *Store) MarkEmailNotificationSent(ctx context.Context, id int64, sentAt time.Time) error {
	result, err := s.db.ExecContext(ctx, "UPDATE email_notifications SET status = 'sent', sent_at = ?, last_error_code = '' WHERE id = ?", unixOrZero(sentAt), id)
	if err != nil {
		return fmt.Errorf("mark email notification sent: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check email notification sent: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) MarkEmailNotificationFailed(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, errorCode string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE email_notifications SET attempts = ?, next_attempt_at = ?, last_error_code = ? WHERE id = ?", attempts, unixOrZero(nextAttemptAt), errorCode, id)
	if err != nil {
		return fmt.Errorf("mark email notification failed: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check email notification failure: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) MarkEmailNotificationCanceled(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "UPDATE email_notifications SET status = 'canceled' WHERE id = ? AND status = 'pending'", id)
	if err != nil {
		return fmt.Errorf("cancel email notification: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check email notification cancellation: %w", err)
	} else if count == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *Store) CancelEmailNotificationsForWorkspace(ctx context.Context, workspaceID string) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE email_notifications SET status = 'canceled' WHERE workspace_id = ? AND status = 'pending'", workspaceID); err != nil {
		return fmt.Errorf("cancel workspace email notifications: %w", err)
	}
	return nil
}

func (s *Store) CancelEmailNotificationsForUser(ctx context.Context, userID string) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE email_notifications SET status = 'canceled' WHERE owner_user_id = ? AND status = 'pending'", userID); err != nil {
		return fmt.Errorf("cancel user email notifications: %w", err)
	}
	return nil
}

const workspaceSelect = `SELECT id, owner_user_id, template_id, name, desired_state, observed_state, runtime_id,
	template_revision, template_configuration_json, template_secrets_json, template_image_reference, template_image_digest, observed_error_code, observed_error, allocated_cpu_millis, allocated_memory_bytes, allocated_storage_bytes, created_at, updated_at,
	observed_at, initial_connection_timeout_seconds, stopped_retention_seconds, data_retention_seconds,
	started_at, last_connected_at, active_sessions, idle_since, stopped_at, container_deleted_at, data_archive_eligible_at,
	operation, operation_status, operation_error, operation_started_at, operation_updated_at FROM workspaces`

func (s *Store) listWorkspaces(ctx context.Context, query string, args ...any) ([]domain.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	workspaces := make([]domain.Workspace, 0)
	for rows.Next() {
		workspace, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspaces: %w", err)
	}
	return workspaces, nil
}

func scanWorkspace(row scanner) (domain.Workspace, error) {
	var (
		workspace                                                                                domain.Workspace
		desiredState                                                                             string
		templateConfiguration                                                                    string
		templateSecrets                                                                          string
		createdUnix                                                                              int64
		updatedUnix                                                                              int64
		observedAtUnix                                                                           int64
		startedUnix, connectedUnix, idleSinceUnix, stoppedUnix, deletedUnix, archiveEligibleUnix int64
		operationStartedUnix, operationUpdatedUnix                                               int64
		operation, operationStatus, operationError                                               string
	)
	if err := row.Scan(&workspace.ID, &workspace.OwnerUserID, &workspace.TemplateID, &workspace.Name, &desiredState,
		&workspace.ObservedState, &workspace.RuntimeID, &workspace.TemplateRevision, &templateConfiguration, &templateSecrets, &workspace.TemplateImageReference, &workspace.TemplateImageDigest, &workspace.ObservedErrorCode, &workspace.ObservedError, &workspace.AllocatedCPUMillis,
		&workspace.AllocatedMemoryBytes, &workspace.AllocatedStorageBytes, &createdUnix, &updatedUnix,
		&observedAtUnix, &workspace.InitialConnectionTimeoutSeconds, &workspace.StoppedRetentionSeconds,
		&workspace.DataRetentionSeconds, &startedUnix, &connectedUnix, &workspace.ActiveSessions, &idleSinceUnix, &stoppedUnix, &deletedUnix, &archiveEligibleUnix,
		&operation, &operationStatus, &operationError, &operationStartedUnix, &operationUpdatedUnix); err != nil {
		if err == sql.ErrNoRows {
			return domain.Workspace{}, repository.ErrNotFound
		}
		return domain.Workspace{}, fmt.Errorf("scan workspace: %w", err)
	}
	workspace.DesiredState = domain.DesiredWorkspaceState(desiredState)
	workspace.CreatedAt = time.Unix(createdUnix, 0).UTC()
	workspace.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	if observedAtUnix != 0 {
		workspace.ObservedAt = time.Unix(observedAtUnix, 0).UTC()
	}
	workspace.StartedAt = timeFromUnix(startedUnix)
	workspace.LastConnectedAt = timeFromUnix(connectedUnix)
	workspace.IdleSince = timeFromUnix(idleSinceUnix)
	workspace.StoppedAt = timeFromUnix(stoppedUnix)
	workspace.ContainerDeletedAt = timeFromUnix(deletedUnix)
	workspace.DataArchiveEligibleAt = timeFromUnix(archiveEligibleUnix)
	workspace.Operation = operation
	workspace.OperationStatus = operationStatus
	workspace.OperationError = operationError
	workspace.OperationStartedAt = timeFromUnix(operationStartedUnix)
	workspace.OperationUpdatedAt = timeFromUnix(operationUpdatedUnix)
	if templateConfiguration != "" && templateConfiguration != "{}" {
		if err := json.Unmarshal([]byte(templateConfiguration), &workspace.TemplateConfiguration); err != nil {
			return domain.Workspace{}, fmt.Errorf("decode workspace template configuration: %w", err)
		}
	}
	if templateSecrets != "" && templateSecrets != "{}" {
		if err := json.Unmarshal([]byte(templateSecrets), &workspace.TemplateSecrets); err != nil {
			return domain.Workspace{}, fmt.Errorf("decode workspace template secrets: %w", err)
		}
	}
	return workspace, nil
}

func scanUserQuota(row scanner) (domain.UserQuota, error) {
	var (
		quota                    domain.UserQuota
		createdUnix, updatedUnix int64
	)
	if err := row.Scan(&quota.UserID, &quota.MaxCPUMillis, &quota.MaxMemoryBytes, &quota.MaxStorageBytes,
		&quota.MaxWorkspaces, &quota.MaxRunningWorkspaces, &createdUnix, &updatedUnix); err != nil {
		if err == sql.ErrNoRows {
			return domain.UserQuota{}, repository.ErrNotFound
		}
		return domain.UserQuota{}, fmt.Errorf("scan user quota: %w", err)
	}
	quota.CreatedAt = time.Unix(createdUnix, 0).UTC()
	quota.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return quota, nil
}

func scanGroupQuota(row scanner) (domain.GroupQuota, error) {
	var quota domain.GroupQuota
	var createdUnix, updatedUnix int64
	if err := row.Scan(&quota.GroupID, &quota.MaxCPUMillis, &quota.MaxMemoryBytes, &quota.MaxStorageBytes,
		&quota.MaxWorkspaces, &quota.MaxRunningWorkspaces, &createdUnix, &updatedUnix); err != nil {
		if err == sql.ErrNoRows {
			return domain.GroupQuota{}, repository.ErrNotFound
		}
		return domain.GroupQuota{}, fmt.Errorf("scan group quota: %w", err)
	}
	quota.CreatedAt = time.Unix(createdUnix, 0).UTC()
	quota.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return quota, nil
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func templateConfigurationJSON(value domain.TemplateConfiguration) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func templateSecretsJSON(value map[string]string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func timeFromUnix(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUserRecord(row scanner) (repository.UserRecord, error) {
	var (
		record      repository.UserRecord
		role        string
		disabled    int
		mustChange  int
		createdUnix int64
		updatedUnix int64
	)
	if err := row.Scan(&record.User.ID, &record.User.Username, &record.User.Email, &record.User.DisplayName, &record.PasswordHash,
		&role, &disabled, &mustChange, &createdUnix, &updatedUnix); err != nil {
		if err == sql.ErrNoRows {
			return repository.UserRecord{}, repository.ErrNotFound
		}
		return repository.UserRecord{}, fmt.Errorf("scan user: %w", err)
	}
	record.User.Role = domain.Role(role)
	record.User.Disabled = disabled != 0
	record.User.MustChangePassword = mustChange != 0
	record.User.CreatedAt = time.Unix(createdUnix, 0).UTC()
	record.User.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return record, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
