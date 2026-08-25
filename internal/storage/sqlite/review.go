package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/audit"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func (s *Store) CreateVessel(ctx context.Context, vessel model.Vessel) (model.Vessel, error) {
	vessel.CreatedAt = s.now()
	vessel.Version = 1
	result, err := s.db.ExecContext(ctx, `INSERT INTO vessels(name,ccs_number,owner,battery_capacity_kwh,version,created_at)
VALUES(?,?,?,?,1,?)`, vessel.Name, vessel.CCSNumber, vessel.Owner, vessel.BatteryCapacity, formatTime(vessel.CreatedAt))
	if err != nil {
		return model.Vessel{}, fault.Wrap(fault.Conflict, "vessel_exists", "vessel identity already exists", err)
	}
	vessel.ID, err = result.LastInsertId()
	if err != nil {
		return model.Vessel{}, fmt.Errorf("read vessel id: %w", err)
	}
	return vessel, nil
}

func (s *Store) Vessel(ctx context.Context, id int64) (model.Vessel, error) {
	var vessel model.Vessel
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,ccs_number,owner,battery_capacity_kwh,version,created_at FROM vessels WHERE id=?`, id).
		Scan(&vessel.ID, &vessel.Name, &vessel.CCSNumber, &vessel.Owner, &vessel.BatteryCapacity, &vessel.Version, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Vessel{}, fault.New(fault.NotFound, "vessel_not_found", "vessel not found")
	}
	if err != nil {
		return model.Vessel{}, fmt.Errorf("load vessel: %w", err)
	}
	vessel.CreatedAt, err = parseTime(created)
	return vessel, err
}

func (s *Store) CreateReview(ctx context.Context, actorID, vesselID int64, notes, requestID string) (model.ClassReview, error) {
	now := s.now()
	var review model.ClassReview
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireVessel(ctx, tx, vesselID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO class_reviews(vessel_id,status,submitted_by,version,notes,created_at,updated_at)
VALUES(?,'draft',?,1,?,?,?)`, vesselID, actorID, notes, formatTime(now), formatTime(now))
		if err != nil {
			return fmt.Errorf("create class review: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read review id: %w", err)
		}
		if err := audit.New().Write(ctx, tx, actorID, "class_review", id, "create", "success", requestID, notes); err != nil {
			return err
		}
		review = model.ClassReview{ID: id, VesselID: vesselID, Status: "draft", SubmittedBy: actorID, Version: 1, Notes: notes, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	return review, err
}

func (s *Store) SubmitReview(ctx context.Context, actorID, reviewID, expectedVersion int64, idemKey, requestID string) (model.ClassReview, error) {
	now := s.now()
	var review model.ClassReview
	replayed := false
	err := s.InTxPhases(ctx, func(tx *sql.Tx) error {
		if idemKey != "" {
			var priorID int64
			err := tx.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE actor_id=? AND method='POST' AND route='/reviews/submit' AND idem_key=? AND expires_at>?`, actorID, idemKey, formatTime(now)).Scan(&priorID)
			if err == nil {
				replayed = true
				review, err = loadReview(ctx, tx, priorID)
				return err
			}
			if err != sql.ErrNoRows {
				return fmt.Errorf("check idempotency: %w", err)
			}
		}
		current, err := loadReview(ctx, tx, reviewID)
		if err != nil {
			return err
		}
		if current.Status != "draft" {
			return fault.New(fault.Conflict, "invalid_review_transition", "only draft reviews can be submitted")
		}
		result, err := tx.ExecContext(ctx, `UPDATE class_reviews SET status='submitted',version=version+1,updated_at=? WHERE id=? AND version=? AND status='draft'`, formatTime(now), reviewID, expectedVersion)
		if err != nil {
			return fmt.Errorf("submit review: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "stale_review", "review was changed by another operator")
		}
		review, err = loadReview(ctx, tx, reviewID)
		return err
	}, func(tx *sql.Tx) error {
		if replayed {
			return nil
		}
		if idemKey != "" {
			_, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(actor_id,method,route,idem_key,response_code,resource_type,resource_id,created_at,expires_at)
VALUES(?,'POST','/reviews/submit',?,200,'class_review',?,?,?)`, actorID, idemKey, reviewID, formatTime(now), formatTime(now.Add(24*time.Hour)))
			if err != nil {
				return fmt.Errorf("record idempotency: %w", err)
			}
		}
		if err := audit.New().Write(ctx, tx, actorID, "class_review", reviewID, "submit", "success", requestID, "submitted for CCS survey"); err != nil {
			return err
		}
		return nil
	})
	return review, err
}

func (s *Store) DecideReview(ctx context.Context, reviewerID, reviewID, expectedVersion int64, decision, reason, requestID string) (model.ClassReview, error) {
	if decision != "approved" && decision != "rejected" {
		return model.ClassReview{}, fault.New(fault.Invalid, "invalid_decision", "decision must be approved or rejected")
	}
	now := s.now()
	var updated model.ClassReview
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := loadReview(ctx, tx, reviewID)
		if err != nil {
			return err
		}
		if current.Status != "submitted" {
			return fault.New(fault.Conflict, "invalid_review_transition", "only submitted reviews can be decided")
		}
		result, err := tx.ExecContext(ctx, `UPDATE class_reviews SET status=?,version=version+1,updated_at=? WHERE id=? AND version=? AND status='submitted'`, decision, formatTime(now), reviewID, expectedVersion)
		if err != nil {
			return fmt.Errorf("decide review: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "stale_review", "review was changed by another surveyor")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_decisions(review_id,reviewer_id,decision,reason,created_at) VALUES(?,?,?,?,?)`, reviewID, reviewerID, decision, reason, formatTime(now)); err != nil {
			return fmt.Errorf("record decision: %w", err)
		}
		if err := audit.New().Write(ctx, tx, reviewerID, "class_review", reviewID, "decide", "success", requestID, decision+": "+reason); err != nil {
			return err
		}
		updated, err = loadReview(ctx, tx, reviewID)
		return err
	})
	return updated, err
}

func (s *Store) Review(ctx context.Context, id int64) (model.ClassReview, error) {
	return loadReview(ctx, s.db, id)
}

func loadReview(ctx context.Context, exec executor, id int64) (model.ClassReview, error) {
	var review model.ClassReview
	var created, updated string
	err := exec.QueryRowContext(ctx, `SELECT id,vessel_id,status,submitted_by,version,notes,created_at,updated_at FROM class_reviews WHERE id=?`, id).
		Scan(&review.ID, &review.VesselID, &review.Status, &review.SubmittedBy, &review.Version, &review.Notes, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ClassReview{}, fault.New(fault.NotFound, "review_not_found", "class review not found")
	}
	if err != nil {
		return model.ClassReview{}, fmt.Errorf("load review: %w", err)
	}
	if review.CreatedAt, err = parseTime(created); err != nil {
		return model.ClassReview{}, err
	}
	if review.UpdatedAt, err = parseTime(updated); err != nil {
		return model.ClassReview{}, err
	}
	return review, nil
}

func requireVessel(ctx context.Context, exec executor, id int64) (int64, error) {
	var version int64
	if err := exec.QueryRowContext(ctx, "SELECT version FROM vessels WHERE id=?", id).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return 0, fault.New(fault.NotFound, "vessel_not_found", "vessel not found")
	} else if err != nil {
		return 0, fmt.Errorf("load vessel: %w", err)
	}
	return version, nil
}
