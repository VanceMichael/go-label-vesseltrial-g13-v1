package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type memoryRepository struct {
	usersByEmail  map[string]model.User
	sessions      map[string]model.Session
	nextUserID    int64
	nextSessionID int64
	createErr     error
	sessionErr    error
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{usersByEmail: map[string]model.User{}, sessions: map[string]model.Session{}, nextUserID: 1, nextSessionID: 1}
}

func (r *memoryRepository) CreateUser(_ context.Context, email, name string, role model.Role, passwordHash string) (model.User, error) {
	if r.createErr != nil {
		return model.User{}, r.createErr
	}
	if _, exists := r.usersByEmail[email]; exists {
		return model.User{}, fault.New(fault.Conflict, "user_exists", "user already exists")
	}
	user := model.User{ID: r.nextUserID, Email: email, Name: name, Role: role, PasswordHash: passwordHash, Active: true}
	r.nextUserID++
	r.usersByEmail[email] = user
	return user, nil
}

func (r *memoryRepository) UserByEmail(_ context.Context, email string) (model.User, error) {
	user, exists := r.usersByEmail[email]
	if !exists {
		return model.User{}, fault.New(fault.NotFound, "user_not_found", "user not found")
	}
	return user, nil
}

func (r *memoryRepository) CreateSession(_ context.Context, userID int64, tokenHash string, expiresAt time.Time) (model.Session, error) {
	if r.sessionErr != nil {
		return model.Session{}, r.sessionErr
	}
	session := model.Session{ID: r.nextSessionID, UserID: userID, TokenHash: tokenHash, ExpiresAt: expiresAt}
	r.nextSessionID++
	r.sessions[tokenHash] = session
	return session, nil
}

func (r *memoryRepository) ResolveSession(_ context.Context, tokenHash string, now time.Time) (model.User, model.Session, error) {
	session, exists := r.sessions[tokenHash]
	if !exists || session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
		return model.User{}, model.Session{}, fault.New(fault.Unauthorized, "invalid_session", "session invalid")
	}
	for _, user := range r.usersByEmail {
		if user.ID == session.UserID && user.Active {
			return user, session, nil
		}
	}
	return model.User{}, model.Session{}, fault.New(fault.Unauthorized, "invalid_session", "session invalid")
}

func (r *memoryRepository) RevokeSession(_ context.Context, tokenHash string, at time.Time) error {
	session, exists := r.sessions[tokenHash]
	if !exists || session.RevokedAt != nil {
		return fault.New(fault.NotFound, "session_not_found", "session not found")
	}
	session.RevokedAt = &at
	r.sessions[tokenHash] = session
	return nil
}

func TestRegisterNormalizesIdentityAndHashesPassword(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, time.Hour)
	user, err := service.Register(context.Background(), "  COORDINATOR@Example.Test ", "  Trial Coordinator  ", "strong-password", model.RoleCoordinator)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.Email != "coordinator@example.test" || user.Name != "Trial Coordinator" {
		t.Fatalf("normalized identity = %q/%q", user.Email, user.Name)
	}
	if user.PasswordHash == "strong-password" || user.PasswordHash == "" {
		t.Fatalf("password was not hashed: %q", user.PasswordHash)
	}
	if user.Role != model.RoleCoordinator || !user.Active {
		t.Fatalf("registered user = %#v", user)
	}
}

func TestRegisterValidatesEveryBusinessRole(t *testing.T) {
	roles := []model.Role{model.RoleSurveyor, model.RoleCoordinator, model.RoleShore, model.RoleQuality}
	for index, role := range roles {
		repository := newMemoryRepository()
		service := New(repository, time.Hour)
		user, err := service.Register(context.Background(), "role"+string(rune('a'+index))+"@example.test", "Role User", "strong-password", role)
		if err != nil {
			t.Fatalf("Register role %q: %v", role, err)
		}
		if user.Role != role {
			t.Errorf("registered role = %q, want %q", user.Role, role)
		}
	}
}

func TestRegisterRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		userName string
		password string
		role     model.Role
	}{
		{name: "email", email: "missing-at", userName: "Valid Name", password: "strong-password", role: model.RoleShore},
		{name: "name", email: "valid@example.test", userName: "x", password: "strong-password", role: model.RoleShore},
		{name: "password", email: "valid@example.test", userName: "Valid Name", password: "short", role: model.RoleShore},
		{name: "role", email: "valid@example.test", userName: "Valid Name", password: "strong-password", role: model.Role("admin")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(newMemoryRepository(), time.Hour)
			_, err := service.Register(context.Background(), test.email, test.userName, test.password, test.role)
			if !fault.IsKind(err, fault.Invalid) {
				t.Fatalf("Register error = %v, want invalid", err)
			}
		})
	}
}

func TestLoginCreatesOpaqueHashedSessionWithExpiry(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, 90*time.Minute)
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	registered, err := service.Register(context.Background(), "shore@example.test", "Shore Operator", "strong-password", model.RoleShore)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	token, user, expiresAt, err := service.Login(context.Background(), " SHORE@example.test ", "strong-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" || token == HashToken(token) {
		t.Fatalf("token is not opaque: %q", token)
	}
	if user.ID != registered.ID || !expiresAt.Equal(now.Add(90*time.Minute)) {
		t.Fatalf("login result user=%#v expiry=%v", user, expiresAt)
	}
	stored, exists := repository.sessions[HashToken(token)]
	if !exists {
		t.Fatal("hashed token was not persisted")
	}
	if stored.TokenHash == token || !stored.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("stored session = %#v", stored)
	}
}

func TestLoginHidesUnknownUserAndWrongPassword(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, time.Hour)
	if _, err := service.Register(context.Background(), "quality@example.test", "Quality Manager", "strong-password", model.RoleQuality); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, credentials := range [][2]string{{"unknown@example.test", "strong-password"}, {"quality@example.test", "wrong-password"}} {
		_, _, _, err := service.Login(context.Background(), credentials[0], credentials[1])
		if !fault.IsKind(err, fault.Unauthorized) {
			t.Fatalf("Login(%q) error = %v, want unauthorized", credentials[0], err)
		}
		_, code, message := fault.Classify(err)
		if code != "invalid_credentials" || message != "email or password is incorrect" {
			t.Fatalf("credential error leaks distinction: %s/%s", code, message)
		}
	}
}

func TestAuthenticateAndLogoutRevokeTheExactSession(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, time.Hour)
	now := time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.Register(context.Background(), "surveyor@example.test", "Class Surveyor", "strong-password", model.RoleSurveyor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	firstToken, _, _, err := service.Login(context.Background(), "surveyor@example.test", "strong-password")
	if err != nil {
		t.Fatalf("first Login: %v", err)
	}
	secondToken, _, _, err := service.Login(context.Background(), "surveyor@example.test", "strong-password")
	if err != nil {
		t.Fatalf("second Login: %v", err)
	}
	user, session, err := service.Authenticate(context.Background(), firstToken)
	if err != nil {
		t.Fatalf("Authenticate first session: %v", err)
	}
	if user.Role != model.RoleSurveyor || session.TokenHash != HashToken(firstToken) {
		t.Fatalf("authenticated identity = %#v session=%#v", user, session)
	}
	if err := service.Logout(context.Background(), firstToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := service.Authenticate(context.Background(), firstToken); !fault.IsKind(err, fault.Unauthorized) {
		t.Fatalf("revoked session error = %v, want unauthorized", err)
	}
	if _, _, err := service.Authenticate(context.Background(), secondToken); err != nil {
		t.Fatalf("second session was incorrectly revoked: %v", err)
	}
}

func TestAuthenticateHonorsClockBasedExpiry(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, 10*time.Minute)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.Register(context.Background(), "expiring@example.test", "Expiring User", "strong-password", model.RoleCoordinator); err != nil {
		t.Fatalf("Register: %v", err)
	}
	token, _, _, err := service.Login(context.Background(), "expiring@example.test", "strong-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	service.SetClock(func() time.Time { return now.Add(10 * time.Minute) })
	if _, _, err := service.Authenticate(context.Background(), token); !fault.IsKind(err, fault.Unauthorized) {
		t.Fatalf("expired Authenticate error = %v, want unauthorized", err)
	}
}

func TestLoginPropagatesSessionPersistenceFailure(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, time.Hour)
	if _, err := service.Register(context.Background(), "persist@example.test", "Persist User", "strong-password", model.RoleCoordinator); err != nil {
		t.Fatalf("Register: %v", err)
	}
	repository.sessionErr = errors.New("database unavailable")
	_, _, _, err := service.Login(context.Background(), "persist@example.test", "strong-password")
	if !errors.Is(err, repository.sessionErr) {
		t.Fatalf("Login error = %v, want %v", err, repository.sessionErr)
	}
}

func TestHashTokenIsStableAndDoesNotExposeToken(t *testing.T) {
	first := HashToken("sensitive-session-token")
	second := HashToken("sensitive-session-token")
	other := HashToken("another-session-token")
	if first != second {
		t.Fatalf("HashToken is unstable: %q != %q", first, second)
	}
	if first == other || first == "sensitive-session-token" || len(first) != 64 {
		t.Fatalf("HashToken output is invalid: %q", first)
	}
}
