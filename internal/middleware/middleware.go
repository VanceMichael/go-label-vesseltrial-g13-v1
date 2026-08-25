package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/auth"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/requestmeta"
)

type userKey struct{}
type tokenKey struct{}

func User(ctx context.Context) (model.User, bool) {
	value, ok := ctx.Value(userKey{}).(model.User)
	return value, ok
}
func Token(ctx context.Context) string { value, _ := ctx.Value(tokenKey{}).(string); return value }

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" {
			raw := make([]byte, 12)
			if _, err := rand.Read(raw); err == nil {
				id = hex.EncodeToString(raw)
			} else {
				id = time.Now().UTC().Format("20060102150405.000000000")
			}
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(requestmeta.WithRequestID(r.Context(), id)))
	})
}

type responseCapture struct {
	http.ResponseWriter
	status int
}

func (w *responseCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseCapture) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func Logging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		capture := &responseCapture{ResponseWriter: w}
		next.ServeHTTP(capture, r)
		logger.Info("http request", "request_id", requestmeta.RequestID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", capture.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

func Recovery(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered", "request_id", requestmeta.RequestID(r.Context()), "panic", recovered, "stack", string(debug.Stack()))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"internal server error"}}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func Authenticate(service *auth.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			writeAuthError(w, r, "missing_session", "bearer session is required")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		user, _, err := service.Authenticate(r.Context(), token)
		if err != nil {
			writeAuthError(w, r, "invalid_session", "session is invalid, expired or revoked")
			return
		}
		ctx := context.WithValue(r.Context(), userKey{}, user)
		ctx = context.WithValue(ctx, tokenKey{}, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeAuthError(w http.ResponseWriter, r *http.Request, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
		"code": code, "message": message, "request_id": requestmeta.RequestID(r.Context()),
	}})
}
