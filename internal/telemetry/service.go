package telemetry

import (
	"context"
	"strings"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type Repository interface {
	CreateStation(context.Context, string, string) (model.ShoreStation, error)
	IngestTelemetry(context.Context, model.TelemetrySample) (model.TelemetrySample, error)
	ListTelemetry(context.Context, int64, time.Time, int, int) ([]model.TelemetrySample, error)
	MarkStaleStations(context.Context, time.Time) (int64, error)
}

type IngestLifecycleHooks struct {
	AfterSampleInsert func()
}

type ingestLifecycleHooksKey struct{}

func WithIngestLifecycleHooks(ctx context.Context, hooks IngestLifecycleHooks) context.Context {
	return context.WithValue(ctx, ingestLifecycleHooksKey{}, hooks)
}

func IngestLifecycleHooksFromContext(ctx context.Context) IngestLifecycleHooks {
	hooks, _ := ctx.Value(ingestLifecycleHooksKey{}).(IngestLifecycleHooks)
	return hooks
}

func (s *Service) List(ctx context.Context, actor model.User, vesselID int64, since time.Time, limit, offset int) ([]model.TelemetrySample, error) {
	if actor.Role != model.RoleShore && actor.Role != model.RoleCoordinator && actor.Role != model.RoleQuality {
		return nil, fault.New(fault.Forbidden, "role_forbidden", "shore, trial or quality role is required")
	}
	return s.repository.ListTelemetry(ctx, vesselID, since, limit, offset)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func New(repository Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) RegisterStation(ctx context.Context, actor model.User, code, name string) (model.ShoreStation, error) {
	if actor.Role != model.RoleShore {
		return model.ShoreStation{}, fault.New(fault.Forbidden, "role_forbidden", "shore operator role is required")
	}
	code, name = strings.TrimSpace(code), strings.TrimSpace(name)
	if code == "" || name == "" {
		return model.ShoreStation{}, fault.New(fault.Invalid, "invalid_station", "station code and name are required")
	}
	return s.repository.CreateStation(ctx, code, name)
}
func (s *Service) Ingest(ctx context.Context, actor model.User, sample model.TelemetrySample) (model.TelemetrySample, error) {
	if actor.Role != model.RoleShore {
		return model.TelemetrySample{}, fault.New(fault.Forbidden, "role_forbidden", "shore operator role is required")
	}
	if sample.VesselID <= 0 || sample.StationID <= 0 || sample.Sequence <= 0 || sample.BatteryPct < 0 || sample.BatteryPct > 100 || sample.SpeedKnots < 0 || sample.Latitude < -90 || sample.Latitude > 90 || sample.Longitude < -180 || sample.Longitude > 180 {
		return model.TelemetrySample{}, fault.New(fault.Invalid, "invalid_telemetry", "telemetry values are outside accepted ranges")
	}
	if sample.ObservedAt.IsZero() {
		sample.ObservedAt = s.now()
	}
	if sample.ObservedAt.After(s.now().Add(2 * time.Minute)) {
		return model.TelemetrySample{}, fault.New(fault.Invalid, "future_telemetry", "observation time is too far in the future")
	}
	return s.repository.IngestTelemetry(ctx, sample)
}
func (s *Service) MarkStale(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, fault.New(fault.Invalid, "invalid_freshness_window", "freshness window must be positive")
	}
	return s.repository.MarkStaleStations(ctx, s.now().Add(-maxAge))
}
