package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/audit"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func (s *Store) CreateDeliveryBatch(ctx context.Context, actorID, vesselID int64, batchNo, requestID string) (model.DeliveryBatch, error) {
	now := s.now()
	var saved model.DeliveryBatch
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireVessel(ctx, tx, vesselID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO delivery_batches(vessel_id,batch_no,status,version,created_at) VALUES(?,?,'preparing',1,?)`, vesselID, batchNo, formatTime(now))
		if err != nil {
			return fault.Wrap(fault.Conflict, "batch_exists", "delivery batch number already exists", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read batch id: %w", err)
		}
		if err = audit.New().Write(ctx, tx, actorID, "delivery_batch", id, "create", "success", requestID, batchNo); err != nil {
			return err
		}
		saved = model.DeliveryBatch{ID: id, VesselID: vesselID, BatchNo: batchNo, Status: "preparing", Version: 1, CreatedAt: now}
		return nil
	})
	return saved, err
}

func (s *Store) EvaluateDelivery(ctx context.Context, actorID, batchID, version int64, release bool, requestID string) (model.DeliveryBatch, error) {
	now := s.now()
	var saved model.DeliveryBatch
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := loadBatch(ctx, tx, batchID)
		if err != nil {
			return err
		}
		if current.Status != "preparing" && current.Status != "quality_gate" {
			return fault.New(fault.Conflict, "invalid_batch_transition", "batch is not awaiting quality gate")
		}
		var completedPlans, openDefects int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM trial_plans WHERE vessel_id=? AND status='completed'`, current.VesselID).Scan(&completedPlans); err != nil {
			return fmt.Errorf("count completed plans: %w", err)
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM defects WHERE vessel_id=? AND status<>'closed'`, current.VesselID).Scan(&openDefects); err != nil {
			return fmt.Errorf("count open defects: %w", err)
		}
		if completedPlans == 0 || openDefects != 0 {
			return fault.New(fault.Conflict, "delivery_gates_failed", "completed trial and closed defects are required")
		}
		target := "quality_gate"
		var releasedBy any
		var releasedAt any
		if release {
			target = "released"
			releasedBy = actorID
			releasedAt = formatTime(now)
		}
		result, err := tx.ExecContext(ctx, `UPDATE delivery_batches SET status=?,version=version+1,released_by=?,released_at=? WHERE id=? AND version=? AND status IN ('preparing','quality_gate')`, target, releasedBy, releasedAt, batchID, version)
		if err != nil {
			return fmt.Errorf("evaluate delivery: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "stale_batch", "delivery batch was changed by another operator")
		}
		if err = audit.New().Write(ctx, tx, actorID, "delivery_batch", batchID, "quality_gate", "success", requestID, target); err != nil {
			return err
		}
		saved, err = loadBatch(ctx, tx, batchID)
		return err
	})
	return saved, err
}

func loadBatch(ctx context.Context, exec executor, id int64) (model.DeliveryBatch, error) {
	var item model.DeliveryBatch
	var releasedBy sql.NullInt64
	var releasedAt sql.NullString
	var created string
	err := exec.QueryRowContext(ctx, `SELECT id,vessel_id,batch_no,status,version,released_by,released_at,created_at FROM delivery_batches WHERE id=?`, id).Scan(&item.ID, &item.VesselID, &item.BatchNo, &item.Status, &item.Version, &releasedBy, &releasedAt, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DeliveryBatch{}, fault.New(fault.NotFound, "batch_not_found", "delivery batch not found")
	}
	if err != nil {
		return model.DeliveryBatch{}, fmt.Errorf("load delivery batch: %w", err)
	}
	item.ReleasedBy = nullableInt(releasedBy)
	if item.ReleasedAt, err = nullableTime(releasedAt); err != nil {
		return model.DeliveryBatch{}, err
	}
	item.CreatedAt, err = parseTime(created)
	return item, err
}
func (s *Store) DeliveryBatch(ctx context.Context, id int64) (model.DeliveryBatch, error) {
	return loadBatch(ctx, s.db, id)
}
