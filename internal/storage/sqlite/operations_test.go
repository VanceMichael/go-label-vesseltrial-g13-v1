package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type trialFixture struct {
	store       *Store
	coordinator model.User
	surveyor    model.User
	shore       model.User
	quality     model.User
	vessel      model.Vessel
	review      model.ClassReview
	plan        model.TrialPlan
	leg         model.VoyageLeg
	station     model.ShoreStation
}

func newTrialFixture(t *testing.T) trialFixture {
	t.Helper()
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator.fixture@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor.fixture@example.test", model.RoleSurveyor)
	shore := createUser(t, store, "shore.fixture@example.test", model.RoleShore)
	quality := createUser(t, store, "quality.fixture@example.test", model.RoleQuality)
	vessel := createVessel(t, store, "OPS")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	plan, leg := scheduledPlan(t, store, vessel, review, coordinator, fixedNow.Add(time.Hour))
	leg, err := store.ReleaseLeg(context.Background(), coordinator.ID, leg.ID, leg.Version, "fixture-release")
	if err != nil {
		t.Fatalf("release fixture leg: %v", err)
	}
	station, err := store.CreateStation(context.Background(), "QZ-01", "Qinzhou Shore Center")
	if err != nil {
		t.Fatalf("create fixture station: %v", err)
	}
	return trialFixture{store: store, coordinator: coordinator, surveyor: surveyor, shore: shore, quality: quality, vessel: vessel, review: review, plan: plan, leg: leg, station: station}
}

func validSample(f trialFixture, sequence int64) model.TelemetrySample {
	legID := f.leg.ID
	return model.TelemetrySample{
		VesselID:   f.vessel.ID,
		LegID:      &legID,
		StationID:  f.station.ID,
		Sequence:   sequence,
		BatteryPct: 83.5,
		SpeedKnots: 7.2,
		Latitude:   21.95,
		Longitude:  108.62,
		ObservedAt: fixedNow.Add(time.Minute),
	}
}

func TestTelemetryIngestUpdatesStationAndPersistsSampleAtomically(t *testing.T) {
	f := newTrialFixture(t)
	saved, err := f.store.IngestTelemetry(context.Background(), validSample(f, 1))
	if err != nil {
		t.Fatalf("IngestTelemetry: %v", err)
	}
	if saved.ID == 0 || !saved.ReceivedAt.Equal(fixedNow) {
		t.Fatalf("saved telemetry metadata = %#v", saved)
	}
	station, err := f.store.Station(context.Background(), f.station.ID)
	if err != nil {
		t.Fatalf("load station: %v", err)
	}
	if station.Status != "online" || station.LastSeenAt == nil || !station.LastSeenAt.Equal(fixedNow) {
		t.Fatalf("station freshness = %#v", station)
	}
	items, err := f.store.ListTelemetry(context.Background(), f.vessel.ID, fixedNow.Add(-time.Hour), 10, 0)
	if err != nil {
		t.Fatalf("list telemetry: %v", err)
	}
	if len(items) != 1 || items[0].Sequence != 1 || items[0].LegID == nil || *items[0].LegID != f.leg.ID {
		t.Fatalf("telemetry list = %#v", items)
	}
}

func TestTelemetryRejectsDuplicateSequenceWithoutChangingFreshness(t *testing.T) {
	f := newTrialFixture(t)
	if _, err := f.store.IngestTelemetry(context.Background(), validSample(f, 2)); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	f.store.SetClock(func() time.Time { return fixedNow.Add(5 * time.Minute) })
	duplicate := validSample(f, 2)
	duplicate.BatteryPct = 20
	_, err := f.store.IngestTelemetry(context.Background(), duplicate)
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("duplicate error = %v, want conflict", err)
	}
	station, err := f.store.Station(context.Background(), f.station.ID)
	if err != nil {
		t.Fatalf("load station: %v", err)
	}
	if station.LastSeenAt == nil || !station.LastSeenAt.Equal(fixedNow) {
		t.Fatalf("duplicate changed station freshness to %v", station.LastSeenAt)
	}
	items, err := f.store.ListTelemetry(context.Background(), f.vessel.ID, fixedNow.Add(-time.Hour), 10, 0)
	if err != nil {
		t.Fatalf("list telemetry: %v", err)
	}
	if len(items) != 1 || items[0].BatteryPct != 83.5 {
		t.Fatalf("duplicate changed telemetry: %#v", items)
	}
}

func TestTelemetryCanceledContextLeavesNoSampleOrFreshnessChange(t *testing.T) {
	f := newTrialFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.store.IngestTelemetry(ctx, validSample(f, 3))
	if err == nil {
		t.Fatal("canceled ingest unexpectedly succeeded")
	}
	var samples int
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM telemetry_samples").Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if samples != 0 {
		t.Fatalf("sample count after canceled ingest = %d, want 0", samples)
	}
	station, err := f.store.Station(context.Background(), f.station.ID)
	if err != nil {
		t.Fatalf("load station: %v", err)
	}
	if station.Status != "offline" || station.LastSeenAt != nil {
		t.Fatalf("canceled ingest changed station: %#v", station)
	}
}

func TestTelemetryRequiresActiveLegForSameVessel(t *testing.T) {
	f := newTrialFixture(t)
	other := createVessel(t, f.store, "OTHER")
	sample := validSample(f, 4)
	sample.VesselID = other.ID
	_, err := f.store.IngestTelemetry(context.Background(), sample)
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("cross-vessel telemetry error = %v, want conflict", err)
	}
	if _, err := f.store.CompleteLeg(context.Background(), f.coordinator.ID, f.leg.ID, f.leg.Version, "complete-leg"); err != nil {
		t.Fatalf("complete leg: %v", err)
	}
	sample = validSample(f, 5)
	_, err = f.store.IngestTelemetry(context.Background(), sample)
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("completed-leg telemetry error = %v, want conflict", err)
	}
}

func TestMarkStaleStationsOnlyChangesOldOnlineStations(t *testing.T) {
	f := newTrialFixture(t)
	if _, err := f.store.IngestTelemetry(context.Background(), validSample(f, 6)); err != nil {
		t.Fatalf("ingest telemetry: %v", err)
	}
	fresh, err := f.store.CreateStation(context.Background(), "QZ-02", "Fresh Station")
	if err != nil {
		t.Fatalf("create fresh station: %v", err)
	}
	if _, err := f.store.db.Exec(`UPDATE shore_stations SET status='online',last_seen_at=? WHERE id=?`, formatTime(fixedNow.Add(9*time.Minute)), fresh.ID); err != nil {
		t.Fatalf("seed fresh station: %v", err)
	}
	changed, err := f.store.MarkStaleStations(context.Background(), fixedNow.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("MarkStaleStations: %v", err)
	}
	if changed != 1 {
		t.Fatalf("stale stations changed = %d, want 1", changed)
	}
	oldStation, _ := f.store.Station(context.Background(), f.station.ID)
	freshStation, _ := f.store.Station(context.Background(), fresh.ID)
	if oldStation.Status != "stale" || freshStation.Status != "online" {
		t.Fatalf("station states old=%q fresh=%q", oldStation.Status, freshStation.Status)
	}
}

func TestTelemetryPaginationFiltersByObservationTime(t *testing.T) {
	f := newTrialFixture(t)
	for sequence := int64(1); sequence <= 5; sequence++ {
		sample := validSample(f, sequence+10)
		sample.ObservedAt = fixedNow.Add(time.Duration(sequence) * time.Minute)
		if _, err := f.store.IngestTelemetry(context.Background(), sample); err != nil {
			t.Fatalf("ingest sequence %d: %v", sequence, err)
		}
	}
	page, err := f.store.ListTelemetry(context.Background(), f.vessel.ID, fixedNow.Add(2*time.Minute), 2, 1)
	if err != nil {
		t.Fatalf("ListTelemetry: %v", err)
	}
	if len(page) != 2 || page[0].Sequence != 14 || page[1].Sequence != 13 {
		t.Fatalf("telemetry page sequences = %v, want 14,13", []int64{page[0].Sequence, page[1].Sequence})
	}
}

func TestDefectLifecycleRequiresOrderedTransitions(t *testing.T) {
	f := newTrialFixture(t)
	legID := f.leg.ID
	opened, err := f.store.CreateDefect(context.Background(), f.shore.ID, model.Defect{
		VesselID: f.vessel.ID, LegID: &legID, Code: "PROP-001", Severity: "major",
		Title: "propulsion inverter temperature", Description: "temperature exceeded trial threshold",
	}, "open-defect")
	if err != nil {
		t.Fatalf("CreateDefect: %v", err)
	}
	if opened.Status != "open" || opened.Version != 1 {
		t.Fatalf("opened defect = %#v", opened)
	}
	_, err = f.store.TransitionDefect(context.Background(), f.quality.ID, opened.ID, opened.Version, "closed", "skip verification", "bad-close")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("skipped transition error = %v, want conflict", err)
	}
	contained, err := f.store.TransitionDefect(context.Background(), f.quality.ID, opened.ID, opened.Version, "contained", "load reduced and inverter isolated", "contain")
	if err != nil {
		t.Fatalf("contain defect: %v", err)
	}
	verified, err := f.store.TransitionDefect(context.Background(), f.quality.ID, contained.ID, contained.Version, "verified", "temperature stable during repeat run", "verify")
	if err != nil {
		t.Fatalf("verify defect: %v", err)
	}
	closed, err := f.store.TransitionDefect(context.Background(), f.quality.ID, verified.ID, verified.Version, "closed", "CCS evidence package accepted", "close")
	if err != nil {
		t.Fatalf("close defect: %v", err)
	}
	if closed.Status != "closed" || closed.Version != 4 {
		t.Fatalf("closed defect = %#v", closed)
	}
	var actions, audits int
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM defect_actions WHERE defect_id=?", opened.ID).Scan(&actions); err != nil {
		t.Fatalf("count actions: %v", err)
	}
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE object_type='defect' AND object_id=?", opened.ID).Scan(&audits); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if actions != 4 || audits != 4 {
		t.Fatalf("defect evidence actions=%d audits=%d, want 4/4", actions, audits)
	}
}

func TestDefectTransitionRollsBackWhenActionWriteFails(t *testing.T) {
	f := newTrialFixture(t)
	opened, err := f.store.CreateDefect(context.Background(), f.shore.ID, model.Defect{VesselID: f.vessel.ID, Code: "NAV-001", Severity: "critical", Title: "navigation sensor disagreement", Description: "dual heading sources diverged"}, "open")
	if err != nil {
		t.Fatalf("open defect: %v", err)
	}
	if _, err := f.store.db.Exec(`CREATE TRIGGER fail_containment BEFORE INSERT ON defect_actions
WHEN NEW.action='contained' BEGIN SELECT RAISE(ABORT, 'action store unavailable'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	_, err = f.store.TransitionDefect(context.Background(), f.quality.ID, opened.ID, opened.Version, "contained", "isolate sensor", "contain")
	if err == nil {
		t.Fatal("transition unexpectedly succeeded")
	}
	loaded, err := f.store.Defect(context.Background(), opened.ID)
	if err != nil {
		t.Fatalf("load defect: %v", err)
	}
	if loaded.Status != "open" || loaded.Version != opened.Version {
		t.Fatalf("failed action left partial transition: %#v", loaded)
	}
}

func TestDefectListFiltersSeverityAndStatus(t *testing.T) {
	f := newTrialFixture(t)
	inputs := []model.Defect{
		{VesselID: f.vessel.ID, Code: "MIN-1", Severity: "minor", Title: "paint mark", Description: "minor marking"},
		{VesselID: f.vessel.ID, Code: "MAJ-1", Severity: "major", Title: "battery cooling", Description: "cooling alarm"},
		{VesselID: f.vessel.ID, Code: "CRT-1", Severity: "critical", Title: "steering feedback", Description: "feedback interruption"},
	}
	for _, input := range inputs {
		if _, err := f.store.CreateDefect(context.Background(), f.shore.ID, input, "open-list"); err != nil {
			t.Fatalf("create defect %s: %v", input.Code, err)
		}
	}
	critical, err := f.store.ListDefects(context.Background(), f.vessel.ID, "open", "critical", 20, 0)
	if err != nil {
		t.Fatalf("list critical defects: %v", err)
	}
	if len(critical) != 1 || critical[0].Code != "CRT-1" {
		t.Fatalf("critical defects = %#v", critical)
	}
	all, err := f.store.ListDefects(context.Background(), f.vessel.ID, "open", "", 2, 0)
	if err != nil {
		t.Fatalf("list open defects: %v", err)
	}
	if len(all) != 2 || all[0].Severity != "critical" || all[1].Severity != "major" {
		t.Fatalf("severity ordering = %#v", all)
	}
}

func TestDeliveryGateRequiresCompletedTrialAndClosedDefects(t *testing.T) {
	f := newTrialFixture(t)
	batch, err := f.store.CreateDeliveryBatch(context.Background(), f.quality.ID, f.vessel.ID, "BG-2026-001", "batch-create")
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	_, err = f.store.EvaluateDelivery(context.Background(), f.quality.ID, batch.ID, batch.Version, true, "gate-before-trial")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("gate before trial error = %v, want conflict", err)
	}
	f.leg, err = f.store.CompleteLeg(context.Background(), f.coordinator.ID, f.leg.ID, f.leg.Version, "complete-trial")
	if err != nil {
		t.Fatalf("complete trial: %v", err)
	}
	defect, err := f.store.CreateDefect(context.Background(), f.shore.ID, model.Defect{VesselID: f.vessel.ID, Code: "DEL-1", Severity: "major", Title: "delivery evidence gap", Description: "missing repeat measurement"}, "open-defect")
	if err != nil {
		t.Fatalf("open defect: %v", err)
	}
	_, err = f.store.EvaluateDelivery(context.Background(), f.quality.ID, batch.ID, batch.Version, true, "gate-open-defect")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("gate with open defect error = %v, want conflict", err)
	}
	for _, target := range []string{"contained", "verified", "closed"} {
		defect, err = f.store.TransitionDefect(context.Background(), f.quality.ID, defect.ID, defect.Version, target, target+" evidence", "transition-"+target)
		if err != nil {
			t.Fatalf("transition defect to %s: %v", target, err)
		}
	}
	released, err := f.store.EvaluateDelivery(context.Background(), f.quality.ID, batch.ID, batch.Version, true, "gate-release")
	if err != nil {
		t.Fatalf("release batch: %v", err)
	}
	if released.Status != "released" || released.ReleasedAt == nil || released.ReleasedBy == nil || *released.ReleasedBy != f.quality.ID {
		t.Fatalf("released batch = %#v", released)
	}
}

func TestDeliveryGateRollbackPreservesPreparingStateOnAuditFailure(t *testing.T) {
	f := newTrialFixture(t)
	var err error
	f.leg, err = f.store.CompleteLeg(context.Background(), f.coordinator.ID, f.leg.ID, f.leg.Version, "complete-trial")
	if err != nil {
		t.Fatalf("complete trial: %v", err)
	}
	batch, err := f.store.CreateDeliveryBatch(context.Background(), f.quality.ID, f.vessel.ID, "BG-2026-002", "create-batch")
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if _, err := f.store.db.Exec(`CREATE TRIGGER fail_delivery_audit BEFORE INSERT ON audit_events
WHEN NEW.object_type='delivery_batch' AND NEW.action='quality_gate' BEGIN SELECT RAISE(ABORT, 'audit down'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	_, err = f.store.EvaluateDelivery(context.Background(), f.quality.ID, batch.ID, batch.Version, true, "release")
	if err == nil {
		t.Fatal("release unexpectedly succeeded")
	}
	loaded, err := f.store.DeliveryBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("load batch: %v", err)
	}
	if loaded.Status != "preparing" || loaded.Version != 1 || loaded.ReleasedAt != nil {
		t.Fatalf("failed gate left partial batch: %#v", loaded)
	}
}

func TestWorkerJobClaimCompleteLifecycle(t *testing.T) {
	store := openTestStore(t)
	job, err := store.EnqueueJob(context.Background(), "telemetry_staleness_check", `{"max_age_seconds":60}`, 3, fixedNow)
	if err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	claimed, err := store.ClaimJob(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if claimed.ID != job.ID || claimed.Status != "running" || claimed.Attempts != 1 || claimed.LockedUntil == nil {
		t.Fatalf("claimed job = %#v", claimed)
	}
	if err := store.CompleteJob(context.Background(), claimed.ID); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	loaded, err := store.Job(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loaded.Status != "succeeded" || loaded.LockedUntil != nil {
		t.Fatalf("completed job = %#v", loaded)
	}
}

func TestWorkerJobRetryThenPermanentFailure(t *testing.T) {
	store := openTestStore(t)
	job, err := store.EnqueueJob(context.Background(), "bad-job", `{}`, 2, fixedNow)
	if err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	claimed, err := store.ClaimJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	firstFailure := errors.New("temporary station outage")
	if err := store.FailJob(context.Background(), claimed.ID, firstFailure, 5*time.Second); err != nil {
		t.Fatalf("first fail: %v", err)
	}
	loaded, err := store.Job(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load retry job: %v", err)
	}
	if loaded.Status != "retry" || loaded.LastError != firstFailure.Error() || !loaded.AvailableAt.Equal(fixedNow.Add(5*time.Second)) {
		t.Fatalf("retry job = %#v", loaded)
	}
	store.SetClock(func() time.Time { return fixedNow.Add(6 * time.Second) })
	claimed, err = store.ClaimJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if err := store.FailJob(context.Background(), claimed.ID, errors.New("station permanently unavailable"), time.Minute); err != nil {
		t.Fatalf("second fail: %v", err)
	}
	loaded, err = store.Job(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load failed job: %v", err)
	}
	if loaded.Status != "failed" || loaded.Attempts != 2 {
		t.Fatalf("permanent job = %#v", loaded)
	}
}

func TestClaimJobSkipsFutureAndLockedWork(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.EnqueueJob(context.Background(), "future", `{}`, 3, fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("enqueue future: %v", err)
	}
	locked, err := store.EnqueueJob(context.Background(), "locked", `{}`, 3, fixedNow)
	if err != nil {
		t.Fatalf("enqueue locked: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE worker_jobs SET status='running',locked_until=? WHERE id=?`, formatTime(fixedNow.Add(time.Hour)), locked.ID); err != nil {
		t.Fatalf("lock job: %v", err)
	}
	_, err = store.ClaimJob(context.Background(), time.Minute)
	if !fault.IsKind(err, fault.NotFound) {
		t.Fatalf("ClaimJob error = %v, want not found", err)
	}
}

func TestJobLeaseAllowsExpiredRetryClaim(t *testing.T) {
	store := openTestStore(t)
	job, err := store.EnqueueJob(context.Background(), "expired-lease", `{}`, 3, fixedNow)
	if err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE worker_jobs SET status='retry',locked_until=? WHERE id=?`, formatTime(fixedNow.Add(-time.Second)), job.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	claimed, err := store.ClaimJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("claim expired lease: %v", err)
	}
	if claimed.ID != job.ID || claimed.Status != "running" {
		t.Fatalf("claimed expired lease = %#v", claimed)
	}
}
