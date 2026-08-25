package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type Repository interface {
	CreateUser(context.Context, string, string, model.Role, string) (model.User, error)
	UserByEmail(context.Context, string) (model.User, error)
	CreateSession(context.Context, int64, string, time.Time) (model.Session, error)
	ResolveSession(context.Context, string, time.Time) (model.User, model.Session, error)
	RevokeSession(context.Context, string, time.Time) error
}

type Service struct {
	repository Repository
	ttl        time.Duration
	now        func() time.Time
}

func New(repository Repository, ttl time.Duration) *Service {
	return &Service{repository: repository, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) Register(ctx context.Context, email, name, password string, role model.Role) (model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if !strings.Contains(email, "@") || len(name) < 2 || len(password) < 10 {
		return model.User{}, fault.New(fault.Invalid, "invalid_registration", "valid email, name and password of at least 10 characters are required")
	}
	if !validRole(role) {
		return model.User{}, fault.New(fault.Invalid, "invalid_role", "unsupported business role")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return model.User{}, fault.Wrap(fault.Internal, "password_hash_failed", "could not secure password", err)
	}
	return s.repository.CreateUser(ctx, email, name, role, string(hash))
}

func (s *Service) Login(ctx context.Context, email, password string) (string, model.User, time.Time, error) {
	user, err := s.repository.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil || !user.Active || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return "", model.User{}, time.Time{}, fault.New(fault.Unauthorized, "invalid_credentials", "email or password is incorrect")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", model.User{}, time.Time{}, fault.Wrap(fault.Internal, "token_generation_failed", "could not create session", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := s.now().Add(s.ttl)
	if _, err := s.repository.CreateSession(ctx, user.ID, HashToken(token), expires); err != nil {
		return "", model.User{}, time.Time{}, err
	}
	return token, user, expires, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (model.User, model.Session, error) {
	if strings.TrimSpace(token) == "" {
		return model.User{}, model.Session{}, fault.New(fault.Unauthorized, "missing_session", "bearer session is required")
	}
	return s.repository.ResolveSession(ctx, HashToken(token), s.now())
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return fault.New(fault.Unauthorized, "missing_session", "bearer session is required")
	}
	return s.repository.RevokeSession(ctx, HashToken(token), s.now())
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validRole(role model.Role) bool {
	switch role {
	case model.RoleSurveyor, model.RoleCoordinator, model.RoleShore, model.RoleQuality:
		return true
	default:
		return false
	}
}
