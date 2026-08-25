package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
	telemetryservice "github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/telemetry"
)

func TestCanceledTelemetryIngestLeavesNoSampleOrFreshnessChange(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "telemetry-cancel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	clock := time.Date(2026, 8, 25, 9, 15, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return clock })
	coordinator, err := store.CreateUser(ctx, "telemetry-coordinator@example.test", "Telemetry Coordinator", model.RoleCoordinator, "hash")
	if err != nil {
		t.Fatalf("create coordinator: %v", err)
	}
	surveyor, err := store.CreateUser(ctx, "telemetry-surveyor@example.test", "Telemetry Surveyor", model.RoleSurveyor, "hash")
	if err != nil {
		t.Fatalf("create surveyor: %v", err)
	}
	vessel, err := store.CreateVessel(ctx, model.Vessel{Name: "Cancellation Vessel", CCSNumber: "CCS-CANCEL-6", Owner: "North Channel Fleet", BatteryCapacity: 4200})
	if err != nil {
		t.Fatalf("create vessel: %v", err)
	}
	review, err := store.CreateReview(ctx, coordinator.ID, vessel.ID, "telemetry approval", "telemetry-review")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	review, err = store.SubmitReview(ctx, coordinator.ID, review.ID, review.Version, "telemetry-submit", "telemetry-submit-request")
	if err != nil {
		t.Fatalf("submit review: %v", err)
	}
	approved, err := store.DecideReview(ctx, surveyor.ID, review.ID, review.Version, "approved", "telemetry approved", "telemetry-decision")
	if err != nil {
		t.Fatalf("approve review: %v", err)
	}
	plan, err := store.CreatePlan(ctx, coordinator.ID, vessel.ID, approved.ID, "telemetry plan", "Asia/Shanghai", "telemetry-plan")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	leg, err := store.AddLeg(ctx, coordinator.ID, plan.ID, model.VoyageLeg{
		Name: "released telemetry leg", Channel: "CANCEL-6", StartsAt: clock.Add(time.Hour), EndsAt: clock.Add(2 * time.Hour),
	}, "telemetry-leg")
	if err != nil {
		t.Fatalf("add leg: %v", err)
	}
	plan, err = store.SchedulePlan(ctx, coordinator.ID, plan.ID, plan.Version, "telemetry-schedule")
	if err != nil {
		t.Fatalf("schedule plan: %v", err)
	}
	leg, err = store.ReleaseLeg(ctx, coordinator.ID, leg.ID, leg.Version, "telemetry-release")
	if err != nil {
		t.Fatalf("release leg: %v", err)
	}
	station, err := store.CreateStation(ctx, "QZ-CANCEL-6", "Cancellation Shore Center")
	if err != nil {
		t.Fatalf("create station: %v", err)
	}
	legID := leg.ID
	sample := model.TelemetrySample{
		VesselID: vessel.ID, LegID: &legID, StationID: station.ID, Sequence: 601,
		BatteryPct: 81.5, SpeedKnots: 6.8, Latitude: 21.95, Longitude: 108.62,
		ObservedAt: clock.Add(time.Minute),
	}
	requestCtx, cancel := context.WithCancel(ctx)
	requestCtx = telemetryservice.WithIngestLifecycleHooks(requestCtx, telemetryservice.IngestLifecycleHooks{AfterSampleInsert: cancel})
	if _, err := store.IngestTelemetry(requestCtx, sample); err == nil {
		t.Fatal("canceled telemetry ingest unexpectedly succeeded")
	}
	var sampleCount int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM telemetry_samples WHERE sequence=?", sample.Sequence).Scan(&sampleCount); err != nil {
		t.Fatalf("count telemetry samples: %v", err)
	}
	if sampleCount != 0 {
		t.Fatalf("canceled ingest left %d telemetry samples", sampleCount)
	}
	loadedStation, err := store.Station(ctx, station.ID)
	if err != nil {
		t.Fatalf("reload station: %v", err)
	}
	if loadedStation.Status != "offline" || loadedStation.LastSeenAt != nil {
		t.Fatalf("canceled ingest changed station freshness: %#v", loadedStation)
	}
	items, err := store.ListTelemetry(ctx, vessel.ID, clock.Add(-time.Hour), 10, 0)
	if err != nil {
		t.Fatalf("list telemetry: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("canceled ingest exposed telemetry samples: %#v", items)
	}
}
