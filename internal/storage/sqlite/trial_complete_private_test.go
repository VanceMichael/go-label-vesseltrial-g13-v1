package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func TestFinalLegAuditFailureKeepsLegAndPlanIncomplete(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "private-final-leg.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	clock := time.Date(2026, 8, 25, 8, 30, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return clock })
	coordinator, err := store.CreateUser(ctx, "final-leg-coordinator@example.test", "Final Leg Coordinator", model.RoleCoordinator, "hash")
	if err != nil {
		t.Fatalf("create coordinator: %v", err)
	}
	surveyor, err := store.CreateUser(ctx, "final-leg-surveyor@example.test", "Final Leg Surveyor", model.RoleSurveyor, "hash")
	if err != nil {
		t.Fatalf("create surveyor: %v", err)
	}
	vessel, err := store.CreateVessel(ctx, model.Vessel{Name: "Final Leg Vessel", CCSNumber: "CCS-FINAL-5", Owner: "North Channel Fleet", BatteryCapacity: 4100})
	if err != nil {
		t.Fatalf("create vessel: %v", err)
	}
	review, err := store.CreateReview(ctx, coordinator.ID, vessel.ID, "final-leg approval", "private-review")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	review, err = store.SubmitReview(ctx, coordinator.ID, review.ID, review.Version, "private-submit", "private-submit-request")
	if err != nil {
		t.Fatalf("submit review: %v", err)
	}
	review, err = store.DecideReview(ctx, surveyor.ID, review.ID, review.Version, "approved", "private approval", "private-decision")
	if err != nil {
		t.Fatalf("approve review: %v", err)
	}
	plan, err := store.CreatePlan(ctx, coordinator.ID, vessel.ID, review.ID, "final-leg plan", "Asia/Shanghai", "private-plan")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	leg, err := store.AddLeg(ctx, coordinator.ID, plan.ID, model.VoyageLeg{
		Name: "last released leg", Channel: "FINAL-5", StartsAt: clock.Add(time.Hour), EndsAt: clock.Add(2 * time.Hour),
	}, "private-leg")
	if err != nil {
		t.Fatalf("add leg: %v", err)
	}
	plan, err = store.SchedulePlan(ctx, coordinator.ID, plan.ID, plan.Version, "private-schedule")
	if err != nil {
		t.Fatalf("schedule plan: %v", err)
	}
	leg, err = store.ReleaseLeg(ctx, coordinator.ID, leg.ID, leg.Version, "private-release")
	if err != nil {
		t.Fatalf("release final leg: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_final_leg_audit BEFORE INSERT ON audit_events
WHEN NEW.object_type='voyage_leg' AND NEW.action='complete'
BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatalf("install audit failure: %v", err)
	}
	if _, err := store.CompleteLeg(ctx, coordinator.ID, leg.ID, leg.Version, "private-complete"); err == nil {
		t.Fatal("completion unexpectedly succeeded while audit was unavailable")
	}
	loadedLeg, err := store.Leg(ctx, leg.ID)
	if err != nil {
		t.Fatalf("reload leg: %v", err)
	}
	if loadedLeg.Status != "released" || loadedLeg.Version != leg.Version {
		t.Fatalf("failed completion changed leg: status=%q version=%d", loadedLeg.Status, loadedLeg.Version)
	}
	loadedPlan, err := store.Plan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if loadedPlan.Status != "scheduled" || loadedPlan.Version != plan.Version {
		t.Fatalf("failed completion changed plan: status=%q version=%d", loadedPlan.Status, loadedPlan.Version)
	}
	var audits int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE object_type='voyage_leg' AND object_id=? AND action='complete'`, leg.ID).Scan(&audits); err != nil {
		t.Fatalf("count completion audit: %v", err)
	}
	if audits != 0 {
		t.Fatalf("failed completion wrote %d audit events", audits)
	}
}
