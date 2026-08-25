package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/auth"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/defect"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/delivery"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/middleware"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/review"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/storage/sqlite"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/telemetry"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/trial"
)

type apiFixture struct {
	server *httptest.Server
	store  *sqlite.Store
}

func newAPIFixture(t *testing.T, maxBytes int64) apiFixture {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	authService := auth.New(store, time.Hour)
	api := New(
		authService,
		review.New(store),
		trial.New(store),
		telemetry.New(store),
		defect.New(store),
		delivery.New(store),
		store,
		maxBytes,
	)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := middleware.RequestID(middleware.Recovery(logger, middleware.Logging(logger, api.Routes())))
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return apiFixture{server: server, store: store}
}

func requestJSON(t *testing.T, method, url string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response, data
}

func registerAndLogin(t *testing.T, fixture apiFixture, email string, role model.Role) string {
	t.Helper()
	registration := map[string]any{"email": email, "name": "Foundation User", "password": "strong-password", "role": role}
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/auth/register", registration, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", response.StatusCode, body)
	}
	response, body = requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/auth/login", map[string]string{"email": email, "password": "strong-password"}, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.StatusCode, body)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if result.Token == "" {
		t.Fatal("login returned empty token")
	}
	return result.Token
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func decodeError(t *testing.T, body []byte) errorResponse {
	t.Helper()
	var result errorResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode error response %q: %v", body, err)
	}
	return result
}

func TestHealthAndReadinessExposeDistinctDependencyChecks(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	health, body := requestJSON(t, http.MethodGet, fixture.server.URL+"/healthz", nil, nil)
	if health.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"alive"`) {
		t.Fatalf("health status=%d body=%s", health.StatusCode, body)
	}
	ready, body := requestJSON(t, http.MethodGet, fixture.server.URL+"/readyz", nil, nil)
	if ready.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ready"`) {
		t.Fatalf("ready status=%d body=%s", ready.StatusCode, body)
	}
	if health.Header.Get("X-Request-ID") == "" || ready.Header.Get("X-Request-ID") == "" {
		t.Fatal("health endpoints did not receive request ids")
	}
}

func TestRequestIDPreservesCallerCorrelationValue(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	response, _ := requestJSON(t, http.MethodGet, fixture.server.URL+"/healthz", nil, map[string]string{"X-Request-ID": "shore-gateway-42"})
	if got := response.Header.Get("X-Request-ID"); got != "shore-gateway-42" {
		t.Fatalf("request id = %q, want shore-gateway-42", got)
	}
}

func TestRegisterRejectsUnknownJSONFields(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	input := map[string]any{
		"email": "operator@example.test", "name": "Shore Operator", "password": "strong-password",
		"role": model.RoleShore, "administrator": true,
	}
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/auth/register", input, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "invalid_json" || problem.Error.RequestID == "" {
		t.Fatalf("error response = %#v", problem)
	}
}

func TestRegisterRejectsMultipleJSONValues(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	body := `{"email":"first@example.test","name":"First User","password":"strong-password","role":"shore_operator"} {}`
	request, err := http.NewRequest(http.MethodPost, fixture.server.URL+"/v1/auth/register", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, data)
	}
	problem := decodeError(t, data)
	if problem.Error.Code != "multiple_json_values" {
		t.Fatalf("error code = %q", problem.Error.Code)
	}
}

func TestMaxRequestBodyIsEnforced(t *testing.T) {
	fixture := newAPIFixture(t, 128)
	input := map[string]any{
		"email": "large@example.test", "name": strings.Repeat("large-name", 40),
		"password": "strong-password", "role": model.RoleCoordinator,
	}
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/auth/register", input, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "invalid_json" {
		t.Fatalf("oversize error = %#v", problem)
	}
}

func TestProtectedRouteReturnsStructuredMissingSession(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/vessels", map[string]any{"name": "test"}, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "missing_session" || problem.Error.Message == "" || problem.Error.RequestID == "" {
		t.Fatalf("missing session response = %#v", problem)
	}
}

func TestProtectedRouteRejectsUnknownBearerSession(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/vessels", map[string]any{"name": "test"}, bearer("unknown-token"))
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "invalid_session" {
		t.Fatalf("invalid session response = %#v", problem)
	}
}

func TestCoordinatorCanRegisterVessel(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	token := registerAndLogin(t, fixture, "coordinator@example.test", model.RoleCoordinator)
	input := map[string]any{
		"name": "Beigang Yunhe 001", "ccs_number": "CCS-2026-001",
		"owner": "Beibu Gulf Port", "battery_capacity_kwh": 4200,
	}
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/vessels", input, bearer(token))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	var vessel model.Vessel
	if err := json.Unmarshal(body, &vessel); err != nil {
		t.Fatalf("decode vessel: %v", err)
	}
	if vessel.ID == 0 || vessel.Version != 1 || vessel.CCSNumber != "CCS-2026-001" {
		t.Fatalf("created vessel = %#v", vessel)
	}
}

func TestShoreOperatorCannotRegisterVessel(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	token := registerAndLogin(t, fixture, "shore@example.test", model.RoleShore)
	input := map[string]any{
		"name": "Beigang Yunhe 001", "ccs_number": "CCS-2026-002",
		"owner": "Beibu Gulf Port", "battery_capacity_kwh": 4200,
	}
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/vessels", input, bearer(token))
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "role_forbidden" {
		t.Fatalf("forbidden response = %#v", problem)
	}
}

func TestDuplicateVesselMapsToConflict(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	token := registerAndLogin(t, fixture, "coordinator@example.test", model.RoleCoordinator)
	input := map[string]any{"name": "Beigang Yunhe 001", "ccs_number": "CCS-DUP", "owner": "Beibu Gulf Port", "battery_capacity_kwh": 4200}
	first, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/vessels", input, bearer(token))
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.StatusCode, body)
	}
	second, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/vessels", input, bearer(token))
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second status=%d body=%s", second.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "vessel_exists" {
		t.Fatalf("duplicate response = %#v", problem)
	}
}

func TestVersionedCommandRequiresIfMatchHeader(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	token := registerAndLogin(t, fixture, "coordinator@example.test", model.RoleCoordinator)
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/reviews/1/submit", nil, bearer(token))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "version_required" {
		t.Fatalf("version response = %#v", problem)
	}
}

func TestInvalidPathIDReturnsStableError(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	token := registerAndLogin(t, fixture, "coordinator@example.test", model.RoleCoordinator)
	headers := bearer(token)
	headers["If-Match"] = "1"
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/legs/not-a-number/release", nil, headers)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "invalid_id" {
		t.Fatalf("path error response = %#v", problem)
	}
}

func TestLogoutRevokesTokenForLaterRequests(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	token := registerAndLogin(t, fixture, "quality@example.test", model.RoleQuality)
	response, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/auth/logout", nil, bearer(token))
	if response.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("logout status=%d body=%q", response.StatusCode, body)
	}
	response, body = requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/delivery-batches", map[string]any{"vessel_id": 1, "batch_no": "B-1"}, bearer(token))
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked request status=%d body=%s", response.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "invalid_session" {
		t.Fatalf("revoked response = %#v", problem)
	}
}

func TestRegistrationConflictReturnsRequestCorrelation(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	input := map[string]any{"email": "duplicate@example.test", "name": "Duplicate User", "password": "strong-password", "role": model.RoleQuality}
	first, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/auth/register", input, nil)
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.StatusCode, body)
	}
	second, body := requestJSON(t, http.MethodPost, fixture.server.URL+"/v1/auth/register", input, map[string]string{"X-Request-ID": "registration-duplicate"})
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second status=%d body=%s", second.StatusCode, body)
	}
	problem := decodeError(t, body)
	if problem.Error.Code != "user_exists" || problem.Error.RequestID != "registration-duplicate" {
		t.Fatalf("conflict response = %#v", problem)
	}
}
