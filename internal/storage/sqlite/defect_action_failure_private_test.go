package sqlite

import (
	"context"
	"testing"

	defectservice "github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/defect"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func TestDefectActionFailureKeepsVerifiedStateAndBlocksDelivery(t *testing.T) {
	f := newTrialFixture(t)
	ctx := context.Background()
	if _, err := f.store.CompleteLeg(ctx, f.coordinator.ID, f.leg.ID, f.leg.Version, "complete-trial"); err != nil {
		t.Fatalf("complete trial: %v", err)
	}
	batch, err := f.store.CreateDeliveryBatch(ctx, f.quality.ID, f.vessel.ID, "GATE-FAIL-009", "create-batch")
	if err != nil {
		t.Fatalf("create delivery batch: %v", err)
	}
	opened, err := f.store.CreateDefect(ctx, f.shore.ID, model.Defect{
		VesselID: f.vessel.ID, Code: "CRIT-009", Severity: "critical",
		Title: "steering feedback gap", Description: "repeat steering evidence is missing",
	}, "open-defect")
	if err != nil {
		t.Fatalf("open defect: %v", err)
	}
	contained, err := f.store.TransitionDefect(ctx, f.quality.ID, opened.ID, opened.Version, "contained", "isolate feedback channel", "contain")
	if err != nil {
		t.Fatalf("contain defect: %v", err)
	}
	verified, err := f.store.TransitionDefect(ctx, f.quality.ID, contained.ID, contained.Version, "verified", "repeat run is stable", "verify")
	if err != nil {
		t.Fatalf("verify defect: %v", err)
	}
	if _, err := f.store.db.Exec(`CREATE TRIGGER fail_closed_defect_audit BEFORE INSERT ON audit_events
WHEN NEW.object_type='defect' AND NEW.action='closed' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatalf("create audit trigger: %v", err)
	}
	service := defectservice.New(f.store)
	_, err = service.Transition(ctx, f.quality, verified.ID, verified.Version, "closed", "CCS evidence package accepted", "close-defect")
	if err == nil {
		t.Fatal("close unexpectedly succeeded while audit was unavailable")
	}
	loaded, err := f.store.Defect(ctx, verified.ID)
	if err != nil {
		t.Fatalf("reload defect: %v", err)
	}
	if loaded.Status != "verified" || loaded.Version != verified.Version {
		t.Fatalf("failed close left defect state=%q version=%d, want verified/%d", loaded.Status, loaded.Version, verified.Version)
	}
	var actions, audits int
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM defect_actions WHERE defect_id=?", verified.ID).Scan(&actions); err != nil {
		t.Fatalf("count defect actions: %v", err)
	}
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE object_type='defect' AND object_id=?", verified.ID).Scan(&audits); err != nil {
		t.Fatalf("count defect audits: %v", err)
	}
	if actions != 3 || audits != 3 {
		t.Fatalf("failed close left evidence actions=%d audits=%d, want 3/3", actions, audits)
	}
	if _, err := f.store.EvaluateDelivery(ctx, f.quality.ID, batch.ID, batch.Version, true, "release-after-failed-close"); !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("delivery gate after failed close error=%v, want conflict", err)
	}
}
