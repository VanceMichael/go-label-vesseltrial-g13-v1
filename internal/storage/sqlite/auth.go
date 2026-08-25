package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func (s *Store) CreateUser(ctx context.Context, email, name string, role model.Role, passwordHash string) (model.User, error) {
	created := s.now()
	result, err := s.db.ExecContext(ctx, `INSERT INTO users(email,name,role,password_hash,active,created_at)
VALUES(?,?,?,?,1,?)`, email, name, role, passwordHash, formatTime(created))
	if err != nil {
		return model.User{}, fault.Wrap(fault.Conflict, "user_exists", "user already exists", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.User{}, fmt.Errorf("read user id: %w", err)
	}
	return model.User{ID: id, Email: email, Name: name, Role: role, PasswordHash: passwordHash, Active: true, CreatedAt: created}, nil
}

func (s *Store) UserByEmail(ctx context.Context, email string) (model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,name,role,password_hash,active,created_at FROM users WHERE email=?`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,name,role,password_hash,active,created_at FROM users WHERE id=?`, id))
}

func scanUser(row *sql.Row) (model.User, error) {
	var user model.User
	var active int
	var created string
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.PasswordHash, &active, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.User{}, fault.New(fault.NotFound, "user_not_found", "user not found")
		}
		return model.User{}, fmt.Errorf("scan user: %w", err)
	}
	user.Active = active == 1
	parsed, err := parseTime(created)
	if err != nil {
		return model.User{}, err
	}
	user.CreatedAt = parsed
	return user, nil
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) (model.Session, error) {
	created := s.now()
	result, err := s.db.ExecContext(ctx, `INSERT INTO sessions(user_id,token_hash,expires_at,created_at)
VALUES(?,?,?,?)`, userID, tokenHash, formatTime(expiresAt), formatTime(created))
	if err != nil {
		return model.Session{}, fmt.Errorf("create session: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Session{}, fmt.Errorf("read session id: %w", err)
	}
	return model.Session{ID: id, UserID: userID, TokenHash: tokenHash, ExpiresAt: expiresAt, CreatedAt: created}, nil
}

func (s *Store) ResolveSession(ctx context.Context, tokenHash string, now time.Time) (model.User, model.Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT u.id,u.email,u.name,u.role,u.password_hash,u.active,u.created_at,
s.id,s.user_id,s.token_hash,s.expires_at,s.revoked_at,s.created_at
FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, tokenHash)
	var user model.User
	var session model.Session
	var active int
	var userCreated, expires, sessionCreated string
	var revoked sql.NullString
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.PasswordHash, &active, &userCreated,
		&session.ID, &session.UserID, &session.TokenHash, &expires, &revoked, &sessionCreated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.User{}, model.Session{}, fault.New(fault.Unauthorized, "invalid_session", "session is invalid")
		}
		return model.User{}, model.Session{}, fmt.Errorf("resolve session: %w", err)
	}
	user.Active = active == 1
	var err error
	if user.CreatedAt, err = parseTime(userCreated); err != nil {
		return model.User{}, model.Session{}, err
	}
	if session.ExpiresAt, err = parseTime(expires); err != nil {
		return model.User{}, model.Session{}, err
	}
	if session.CreatedAt, err = parseTime(sessionCreated); err != nil {
		return model.User{}, model.Session{}, err
	}
	if session.RevokedAt, err = nullableTime(revoked); err != nil {
		return model.User{}, model.Session{}, err
	}
	if !user.Active || session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
		return model.User{}, model.Session{}, fault.New(fault.Unauthorized, "expired_session", "session is expired or revoked")
	}
	return user, session, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`, formatTime(at), tokenHash)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revoked rows: %w", err)
	}
	if changed == 0 {
		return fault.New(fault.NotFound, "session_not_found", "active session not found")
	}
	return nil
}
