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

func (s *Store) CreatePlan(ctx context.Context, actorID, vesselID, reviewID int64, name, timezone, requestID string) (model.TrialPlan, error) {
	now := s.now()
	var plan model.TrialPlan
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		review, err := loadReview(ctx, tx, reviewID)
		if err != nil {
			return err
		}
		if review.VesselID != vesselID || review.Status != "approved" {
			return fault.New(fault.Conflict, "review_not_approved", "an approved class review for this vessel is required")
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO trial_plans(vessel_id,review_id,name,status,timezone,version,created_by,created_at,updated_at)
VALUES(?,?,?,'draft',?,1,?,?,?)`, vesselID, reviewID, name, timezone, actorID, formatTime(now), formatTime(now))
		if err != nil {
			return fault.Wrap(fault.Conflict, "plan_exists", "trial plan already exists", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read plan id: %w", err)
		}
		if err := audit.New().Write(ctx, tx, actorID, "trial_plan", id, "create", "success", requestID, name); err != nil {
			return err
		}
		plan = model.TrialPlan{ID: id, VesselID: vesselID, ReviewID: reviewID, Name: name, Status: "draft", Timezone: timezone, Version: 1, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	return plan, err
}

func (s *Store) AddLeg(ctx context.Context, actorID, planID int64, leg model.VoyageLeg, requestID string) (model.VoyageLeg, error) {
	var created model.VoyageLeg
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		plan, err := loadPlan(ctx, tx, planID)
		if err != nil {
			return err
		}
		if plan.Status != "draft" {
			return fault.New(fault.Conflict, "plan_not_draft", "legs can only be added to a draft plan")
		}
		if !leg.StartsAt.Before(leg.EndsAt) {
			return fault.New(fault.Invalid, "invalid_leg_window", "leg start must be before end")
		}
		var overlaps int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM voyage_legs WHERE plan_id=? AND starts_at<? AND ends_at>?`, planID, formatTime(leg.EndsAt), formatTime(leg.StartsAt)).Scan(&overlaps)
		if err != nil {
			return fmt.Errorf("check leg overlap: %w", err)
		}
		if overlaps != 0 {
			return fault.New(fault.Conflict, "leg_window_conflict", "leg overlaps another leg in the plan")
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO voyage_legs(plan_id,vessel_id,name,channel,starts_at,ends_at,status,version)
VALUES(?,?,?,?,?,?,'planned',1)`, planID, plan.VesselID, leg.Name, leg.Channel, formatTime(leg.StartsAt), formatTime(leg.EndsAt))
		if err != nil {
			return fault.Wrap(fault.Conflict, "leg_exists", "voyage leg already exists", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read leg id: %w", err)
		}
		if err := audit.New().Write(ctx, tx, actorID, "voyage_leg", id, "create", "success", requestID, leg.Channel); err != nil {
			return err
		}
		created = leg
		created.ID, created.PlanID, created.VesselID, created.Status, created.Version = id, planID, plan.VesselID, "planned", 1
		return nil
	})
	return created, err
}

func (s *Store) SchedulePlan(ctx context.Context, actorID, planID, expectedVersion int64, requestID string) (model.TrialPlan, error) {
	now := s.now()
	var plan model.TrialPlan
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := loadPlan(ctx, tx, planID)
		if err != nil {
			return err
		}
		if current.Status != "draft" {
			return fault.New(fault.Conflict, "invalid_plan_transition", "only a draft plan can be scheduled")
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM voyage_legs WHERE plan_id=?", planID).Scan(&count); err != nil {
			return fmt.Errorf("count voyage legs: %w", err)
		}
		if count == 0 {
			return fault.New(fault.Conflict, "plan_has_no_legs", "at least one voyage leg is required")
		}
		result, err := tx.ExecContext(ctx, `UPDATE trial_plans SET status='scheduled',version=version+1,updated_at=? WHERE id=? AND version=? AND status='draft'`, formatTime(now), planID, expectedVersion)
		if err != nil {
			return fmt.Errorf("schedule plan: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "stale_plan", "trial plan was changed by another operator")
		}
		if err := audit.New().Write(ctx, tx, actorID, "trial_plan", planID, "schedule", "success", requestID, "voyage legs frozen"); err != nil {
			return err
		}
		plan, err = loadPlan(ctx, tx, planID)
		return err
	})
	return plan, err
}

func (s *Store) ReleaseLeg(ctx context.Context, actorID, legID, expectedVersion int64, requestID string) (model.VoyageLeg, error) {
	now := s.now()
	var leg model.VoyageLeg
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := loadLeg(ctx, tx, legID)
		if err != nil {
			return err
		}
		plan, err := loadPlan(ctx, tx, current.PlanID)
		if err != nil {
			return err
		}
		if plan.Status != "scheduled" && plan.Status != "active" {
			return fault.New(fault.Conflict, "plan_not_scheduled", "plan must be scheduled before leg release")
		}
		if current.Status != "planned" && current.Status != "held" {
			return fault.New(fault.Conflict, "invalid_leg_transition", "only planned or held legs can be released")
		}
		var conflicts int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM voyage_legs WHERE vessel_id=? AND id<>? AND status IN ('released','underway') AND starts_at<? AND ends_at>?`, current.VesselID, legID, formatTime(current.EndsAt), formatTime(current.StartsAt)).Scan(&conflicts)
		if err != nil {
			return fmt.Errorf("check release window: %w", err)
		}
		if conflicts != 0 {
			return fault.New(fault.Conflict, "active_window_conflict", "another active leg overlaps this vessel window")
		}
		result, err := tx.ExecContext(ctx, `UPDATE voyage_legs SET status='released',version=version+1,released_by=? WHERE id=? AND version=? AND status IN ('planned','held')`, actorID, legID, expectedVersion)
		if err != nil {
			return fmt.Errorf("release leg: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "stale_leg", "voyage leg was changed by another operator")
		}
		if plan.Status == "scheduled" {
			if _, err := tx.ExecContext(ctx, `UPDATE trial_plans SET status='active',version=version+1,updated_at=? WHERE id=? AND status='scheduled'`, formatTime(now), plan.ID); err != nil {
				return fmt.Errorf("activate plan: %w", err)
			}
		}
		if err := audit.New().Write(ctx, tx, actorID, "voyage_leg", legID, "release", "success", requestID, current.Channel); err != nil {
			return err
		}
		leg, err = loadLeg(ctx, tx, legID)
		return err
	})
	return leg, err
}

func (s *Store) CompleteLeg(ctx context.Context, actorID, legID, expectedVersion int64, requestID string) (model.VoyageLeg, error) {
	var leg model.VoyageLeg
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := loadLeg(ctx, tx, legID)
		if err != nil {
			return err
		}
		if current.Status != "underway" && current.Status != "released" {
			return fault.New(fault.Conflict, "invalid_leg_transition", "only released or underway legs can complete")
		}
		result, err := tx.ExecContext(ctx, `UPDATE voyage_legs SET status='completed',version=version+1 WHERE id=? AND version=? AND status IN ('released','underway')`, legID, expectedVersion)
		if err != nil {
			return fmt.Errorf("complete leg: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "stale_leg", "voyage leg was changed by another operator")
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM voyage_legs WHERE plan_id=? AND status<>'completed'`, current.PlanID).Scan(&remaining); err != nil {
			return fmt.Errorf("count incomplete legs: %w", err)
		}
		if remaining == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE trial_plans SET status='completed',version=version+1,updated_at=? WHERE id=?`, formatTime(s.now()), current.PlanID); err != nil {
				return fmt.Errorf("complete plan: %w", err)
			}
		}
		leg, err = loadLeg(ctx, tx, legID)
		return err
	})
	if err != nil {
		return model.VoyageLeg{}, err
	}
	if err := audit.New().WriteDB(ctx, s.db, actorID, "voyage_leg", legID, "complete", "success", requestID, "trial observations accepted"); err != nil {
		return model.VoyageLeg{}, err
	}
	return leg, nil
}

func (s *Store) Plan(ctx context.Context, id int64) (model.TrialPlan, error) {
	return loadPlan(ctx, s.db, id)
}
func (s *Store) Leg(ctx context.Context, id int64) (model.VoyageLeg, error) {
	return loadLeg(ctx, s.db, id)
}

func loadPlan(ctx context.Context, exec executor, id int64) (model.TrialPlan, error) {
	var plan model.TrialPlan
	var created, updated string
	err := exec.QueryRowContext(ctx, `SELECT id,vessel_id,review_id,name,status,timezone,version,created_by,created_at,updated_at FROM trial_plans WHERE id=?`, id).
		Scan(&plan.ID, &plan.VesselID, &plan.ReviewID, &plan.Name, &plan.Status, &plan.Timezone, &plan.Version, &plan.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TrialPlan{}, fault.New(fault.NotFound, "plan_not_found", "trial plan not found")
	}
	if err != nil {
		return model.TrialPlan{}, fmt.Errorf("load trial plan: %w", err)
	}
	if plan.CreatedAt, err = parseTime(created); err != nil {
		return model.TrialPlan{}, err
	}
	if plan.UpdatedAt, err = parseTime(updated); err != nil {
		return model.TrialPlan{}, err
	}
	return plan, nil
}

func loadLeg(ctx context.Context, exec executor, id int64) (model.VoyageLeg, error) {
	var leg model.VoyageLeg
	var starts, ends string
	var released sql.NullInt64
	err := exec.QueryRowContext(ctx, `SELECT id,plan_id,vessel_id,name,channel,starts_at,ends_at,status,version,released_by FROM voyage_legs WHERE id=?`, id).
		Scan(&leg.ID, &leg.PlanID, &leg.VesselID, &leg.Name, &leg.Channel, &starts, &ends, &leg.Status, &leg.Version, &released)
	if errors.Is(err, sql.ErrNoRows) {
		return model.VoyageLeg{}, fault.New(fault.NotFound, "leg_not_found", "voyage leg not found")
	}
	if err != nil {
		return model.VoyageLeg{}, fmt.Errorf("load voyage leg: %w", err)
	}
	if leg.StartsAt, err = parseTime(starts); err != nil {
		return model.VoyageLeg{}, err
	}
	if leg.EndsAt, err = parseTime(ends); err != nil {
		return model.VoyageLeg{}, err
	}
	leg.ReleasedBy = nullableInt(released)
	return leg, nil
}

func (s *Store) ListLegs(ctx context.Context, vesselID int64, status string, limit, offset int) ([]model.VoyageLeg, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := `SELECT id,plan_id,vessel_id,name,channel,starts_at,ends_at,status,version,released_by FROM voyage_legs WHERE vessel_id=?`
	args := []any{vesselID}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY starts_at,id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list voyage legs: %w", err)
	}
	defer rows.Close()
	result := make([]model.VoyageLeg, 0)
	for rows.Next() {
		var leg model.VoyageLeg
		var starts, ends string
		var released sql.NullInt64
		if err := rows.Scan(&leg.ID, &leg.PlanID, &leg.VesselID, &leg.Name, &leg.Channel, &starts, &ends, &leg.Status, &leg.Version, &released); err != nil {
			return nil, fmt.Errorf("scan voyage leg: %w", err)
		}
		leg.StartsAt, err = time.Parse(time.RFC3339Nano, starts)
		if err != nil {
			return nil, err
		}
		leg.EndsAt, err = time.Parse(time.RFC3339Nano, ends)
		if err != nil {
			return nil, err
		}
		leg.ReleasedBy = nullableInt(released)
		result = append(result, leg)
	}
	return result, rows.Err()
}
