package sqlite

import (
	"context"
	"testing"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/defect"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/delivery"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func TestConcurrentCriticalDefectCannotCrossDeliveryReleaseGate(t *testing.T) {
	f := newTrialFixture(t)
	f.leg, _ = f.store.CompleteLeg(context.Background(), f.coordinator.ID, f.leg.ID, f.leg.Version, "complete-for-delivery")
	batch, err := f.store.CreateDeliveryBatch(context.Background(), f.quality.ID, f.vessel.ID, "BG-RACE-001", "create-race-batch")
	if err != nil {
		t.Fatalf("create delivery batch: %v", err)
	}
	gate := delivery.New(f.store)
	defects := defect.New(f.store)
	gateChecked := make(chan struct{})
	defectDone := make(chan error, 1)
	oldGateHook, oldDefectHook := deliveryGateAfterQualification, defectBeforeCreate
	defer func() { deliveryGateAfterQualification, defectBeforeCreate = oldGateHook, oldDefectHook }()
	deliveryGateAfterQualification = func() {
		select {
		case <-gateChecked:
		default:
			close(gateChecked)
		}
		if err := <-defectDone; err != nil {
			t.Errorf("concurrent critical defect: %v", err)
		}
	}
	defectBeforeCreate = func() {}
	gateResult := make(chan struct {
		batch model.DeliveryBatch
		err   error
	}, 1)
	go func() {
		result, gateErr := gate.Gate(context.Background(), f.quality, batch.ID, batch.Version, true, "race-release")
		gateResult <- struct {
			batch model.DeliveryBatch
			err   error
		}{result, gateErr}
	}()
	<-gateChecked
	go func() {
		_, openErr := defects.Open(context.Background(), f.shore, model.Defect{VesselID: f.vessel.ID, Code: "CRT-RACE", Severity: "critical", Title: "emergency steering alarm", Description: "critical alarm opened during release qualification"}, "race-defect")
		defectDone <- openErr
	}()
	result := <-gateResult
	if result.err == nil {
		t.Fatal("release gate succeeded while a critical defect was opened during qualification")
	}
	loaded, err := f.store.DeliveryBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("load released batch: %v", err)
	}
	if loaded.Status != "preparing" {
		t.Fatalf("persisted batch status = %q, want preparing", loaded.Status)
	}
	open, err := f.store.ListDefects(context.Background(), f.vessel.ID, "open", "critical", 20, 0)
	if err != nil {
		t.Fatalf("list critical defects: %v", err)
	}
	if len(open) != 1 || open[0].Code != "CRT-RACE" {
		t.Fatalf("critical defects after release = %#v, want the newly opened defect", open)
	}
}
