package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	row := s.db.QueryRowContext(ctx, `SELECT id, username, display_name, password_hash, role, disabled, created_at, updated_at
		FROM users WHERE username = ?`, username)
	return scanUserRecord(row)
}

func (s *Store) FindUserByID(ctx context.Context, id string) (domain.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, display_name, password_hash, role, disabled, created_at, updated_at
		FROM users WHERE id = ?`, id)
	record, err := scanUserRecord(row)
	if err != nil {
		return domain.User{}, err
	}
	return record.User, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, display_name, password_hash, role, disabled, created_at, updated_at
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
		(id, username, display_name, password_hash, role, disabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, user.ID, user.Username, user.DisplayName, passwordHash,
		user.Role, boolInt(user.Disabled), user.CreatedAt.Unix(), user.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *Store) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	result, err := s.db.ExecContext(ctx, "UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?", boolInt(disabled), time.Now().UTC().Unix(), id)
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
	row := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.display_name, u.password_hash, u.role, u.disabled, u.created_at, u.updated_at
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

type scanner interface {
	Scan(dest ...any) error
}

func scanUserRecord(row scanner) (repository.UserRecord, error) {
	var (
		record      repository.UserRecord
		role        string
		disabled    int
		createdUnix int64
		updatedUnix int64
	)
	if err := row.Scan(&record.User.ID, &record.User.Username, &record.User.DisplayName, &record.PasswordHash,
		&role, &disabled, &createdUnix, &updatedUnix); err != nil {
		if err == sql.ErrNoRows {
			return repository.UserRecord{}, repository.ErrNotFound
		}
		return repository.UserRecord{}, fmt.Errorf("scan user: %w", err)
	}
	record.User.Role = domain.Role(role)
	record.User.Disabled = disabled != 0
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
