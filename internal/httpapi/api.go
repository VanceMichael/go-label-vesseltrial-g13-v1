package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/auth"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/defect"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/delivery"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/middleware"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/requestmeta"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/review"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/telemetry"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/trial"
)

type Readiness interface{ Ping(context.Context) error }
type API struct {
	auth       *auth.Service
	reviews    *review.Service
	trials     *trial.Service
	telemetry  *telemetry.Service
	defects    *defect.Service
	deliveries *delivery.Service
	readiness  Readiness
	maxBytes   int64
}

func New(a *auth.Service, r *review.Service, t *trial.Service, tm *telemetry.Service, d *defect.Service, dl *delivery.Service, ready Readiness, maxBytes int64) *API {
	return &API{auth: a, reviews: r, trials: t, telemetry: tm, defects: d, deliveries: dl, readiness: ready, maxBytes: maxBytes}
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.ready)
	mux.HandleFunc("POST /v1/auth/register", a.register)
	mux.HandleFunc("POST /v1/auth/login", a.login)
	protected := http.NewServeMux()
	protected.HandleFunc("POST /v1/auth/logout", a.logout)
	protected.HandleFunc("POST /v1/vessels", a.createVessel)
	protected.HandleFunc("POST /v1/reviews", a.createReview)
	protected.HandleFunc("POST /v1/reviews/{id}/submit", a.submitReview)
	protected.HandleFunc("POST /v1/reviews/{id}/decision", a.decideReview)
	protected.HandleFunc("POST /v1/plans", a.createPlan)
	protected.HandleFunc("POST /v1/plans/{id}/legs", a.addLeg)
	protected.HandleFunc("POST /v1/plans/{id}/schedule", a.schedulePlan)
	protected.HandleFunc("POST /v1/legs/{id}/release", a.releaseLeg)
	protected.HandleFunc("POST /v1/legs/{id}/complete", a.completeLeg)
	protected.HandleFunc("GET /v1/vessels/{id}/legs", a.listLegs)
	protected.HandleFunc("POST /v1/stations", a.createStation)
	protected.HandleFunc("POST /v1/telemetry", a.ingestTelemetry)
	protected.HandleFunc("GET /v1/vessels/{id}/telemetry", a.listTelemetry)
	protected.HandleFunc("POST /v1/defects", a.createDefect)
	protected.HandleFunc("POST /v1/defects/{id}/transition", a.transitionDefect)
	protected.HandleFunc("GET /v1/vessels/{id}/defects", a.listDefects)
	protected.HandleFunc("POST /v1/delivery-batches", a.createBatch)
	protected.HandleFunc("POST /v1/delivery-batches/{id}/gate", a.gateBatch)
	mux.Handle("/v1/", middleware.Authenticate(a.auth, protected))
	return mux
}

func pagination(r *http.Request) (int, int, error) {
	limit, offset := 50, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			return 0, 0, fault.New(fault.Invalid, "invalid_limit", "limit must be between 1 and 500")
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, fault.New(fault.Invalid, "invalid_offset", "offset must be non-negative")
		}
	}
	return limit, offset, nil
}

func (a *API) listLegs(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	vesselID, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	items, err := a.trials.ListLegs(r.Context(), user, vesselID, r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (a *API) listTelemetry(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	vesselID, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	if raw := r.URL.Query().Get("since"); raw != "" {
		since, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			a.writeError(w, r, fault.New(fault.Invalid, "invalid_since", "since must be RFC3339"))
			return
		}
	}
	items, err := a.telemetry.List(r.Context(), user, vesselID, since, limit, offset)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (a *API) listDefects(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	vesselID, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	items, err := a.defects.List(r.Context(), user, vesselID, r.URL.Query().Get("status"), r.URL.Query().Get("severity"), limit, offset)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

type errorResponse struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func (a *API) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	kind, code, message := fault.Classify(err)
	status := http.StatusInternalServerError
	switch kind {
	case fault.Invalid:
		status = http.StatusBadRequest
	case fault.Unauthorized:
		status = http.StatusUnauthorized
	case fault.Forbidden:
		status = http.StatusForbidden
	case fault.NotFound:
		status = http.StatusNotFound
	case fault.Conflict:
		status = http.StatusConflict
	case fault.Unavailable:
		status = http.StatusServiceUnavailable
	}
	var body errorResponse
	body.Error.Code, body.Error.Message, body.Error.RequestID = code, message, requestmeta.RequestID(r.Context())
	a.writeJSON(w, status, body)
}
func (a *API) decode(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, a.maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fault.Wrap(fault.Invalid, "invalid_json", "request body is not valid JSON", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fault.New(fault.Invalid, "multiple_json_values", "request body must contain one JSON value")
	}
	return nil
}
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fault.New(fault.Invalid, "invalid_id", "path id must be a positive integer")
	}
	return id, nil
}
func actor(r *http.Request) (model.User, error) {
	user, ok := middleware.User(r.Context())
	if !ok {
		return model.User{}, fault.New(fault.Unauthorized, "missing_identity", "authenticated identity is missing")
	}
	return user, nil
}
func version(r *http.Request) (int64, error) {
	value, err := strconv.ParseInt(r.Header.Get("If-Match"), 10, 64)
	if err != nil || value <= 0 {
		return 0, fault.New(fault.Invalid, "version_required", "If-Match must contain the current positive version")
	}
	return value, nil
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := a.readiness.Ping(ctx); err != nil {
		a.writeError(w, r, fault.Wrap(fault.Unavailable, "database_unready", "database is not ready", err))
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string     `json:"email"`
		Name     string     `json:"name"`
		Password string     `json:"password"`
		Role     model.Role `json:"role"`
	}
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	user, err := a.auth.Register(r.Context(), input.Email, input.Name, input.Password, input.Role)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, user)
}
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	token, user, expires, err := a.auth.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires, "user": user})
}
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.Logout(r.Context(), middleware.Token(r.Context())); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) createVessel(w http.ResponseWriter, r *http.Request) {
	user, err := actor(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var input model.Vessel
	if err = a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.reviews.RegisterVessel(r.Context(), user, input)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, saved)
}
func (a *API) createReview(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	var input struct {
		VesselID int64  `json:"vessel_id"`
		Notes    string `json:"notes"`
	}
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.reviews.Draft(r.Context(), user, input.VesselID, input.Notes, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, saved)
}
func (a *API) submitReview(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	id, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	v, err := version(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.reviews.Submit(r.Context(), user, id, v, r.Header.Get("Idempotency-Key"), requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, saved)
}
func (a *API) decideReview(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	id, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	v, err := version(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var input struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err = a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.reviews.Decide(r.Context(), user, id, v, input.Decision, input.Reason, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, saved)
}
func (a *API) createPlan(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	var input struct {
		VesselID int64  `json:"vessel_id"`
		ReviewID int64  `json:"review_id"`
		Name     string `json:"name"`
		Timezone string `json:"timezone"`
	}
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.trials.CreatePlan(r.Context(), user, input.VesselID, input.ReviewID, input.Name, input.Timezone, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, saved)
}
func (a *API) addLeg(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	id, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var input model.VoyageLeg
	if err = a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.trials.AddLeg(r.Context(), user, id, input, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, saved)
}
func (a *API) schedulePlan(w http.ResponseWriter, r *http.Request) {
	a.planCommand(w, r, a.trials.Schedule)
}
func (a *API) releaseLeg(w http.ResponseWriter, r *http.Request) {
	a.legCommand(w, r, a.trials.ReleaseWithWindowPrecheck)
}
func (a *API) completeLeg(w http.ResponseWriter, r *http.Request) {
	a.legCommand(w, r, a.trials.Complete)
}
func (a *API) planCommand(w http.ResponseWriter, r *http.Request, command func(context.Context, model.User, int64, int64, string) (model.TrialPlan, error)) {
	user, _ := actor(r)
	id, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	v, err := version(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := command(r.Context(), user, id, v, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, saved)
}
func (a *API) legCommand(w http.ResponseWriter, r *http.Request, command func(context.Context, model.User, int64, int64, string) (model.VoyageLeg, error)) {
	user, _ := actor(r)
	id, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	v, err := version(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := command(r.Context(), user, id, v, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, saved)
}
func (a *API) createStation(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	var input struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.telemetry.RegisterStation(r.Context(), user, input.Code, input.Name)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, saved)
}
func (a *API) ingestTelemetry(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	var input model.TelemetrySample
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.telemetry.Ingest(r.Context(), user, input)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusAccepted, saved)
}
func (a *API) createDefect(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	var input model.Defect
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.defects.Open(r.Context(), user, input, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, saved)
}
func (a *API) transitionDefect(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	id, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	v, err := version(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var input struct {
		Target string `json:"target"`
		Note   string `json:"note"`
	}
	if err = a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.defects.Transition(r.Context(), user, id, v, input.Target, input.Note, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, saved)
}
func (a *API) createBatch(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	var input struct {
		VesselID int64  `json:"vessel_id"`
		BatchNo  string `json:"batch_no"`
	}
	if err := a.decode(w, r, &input); err != nil {
		a.writeError(w, r, err)
		return
	}
	saved, err := a.deliveries.Create(r.Context(), user, input.VesselID, input.BatchNo, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, saved)
}
func (a *API) gateBatch(w http.ResponseWriter, r *http.Request) {
	user, _ := actor(r)
	id, err := pathID(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	v, err := version(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	release := strings.EqualFold(r.URL.Query().Get("release"), "true")
	saved, err := a.deliveries.Gate(r.Context(), user, id, v, release, requestmeta.RequestID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, saved)
}
