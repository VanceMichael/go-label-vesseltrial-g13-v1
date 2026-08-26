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

func (s *Store) CreateDefect(ctx context.Context, actorID int64, input model.Defect, requestID string) (model.Defect, error) {
	now := s.now()
	var saved model.Defect
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireVessel(ctx, tx, input.VesselID); err != nil {
			return err
		}
		if input.LegID != nil {
			leg, err := loadLeg(ctx, tx, *input.LegID)
			if err != nil {
				return err
			}
			if leg.VesselID != input.VesselID {
				return fault.New(fault.Conflict, "defect_leg_mismatch", "defect leg belongs to another vessel")
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO defects(vessel_id,leg_id,code,severity,title,description,status,version,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,'open',1,?,?,?)`, input.VesselID, input.LegID, input.Code, input.Severity, input.Title, input.Description, actorID, formatTime(now), formatTime(now))
		if err != nil {
			return fault.Wrap(fault.Conflict, "defect_exists", "defect code already exists for vessel", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read defect id: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO defect_actions(defect_id,actor_id,action,note,created_at) VALUES(?,?,'open',?,?)`, id, actorID, input.Description, formatTime(now)); err != nil {
			return fmt.Errorf("record defect open action: %w", err)
		}
		if err = audit.New().Write(ctx, tx, actorID, "defect", id, "open", "success", requestID, input.Severity); err != nil {
			return err
		}
		input.ID, input.Status, input.Version, input.CreatedBy, input.CreatedAt, input.UpdatedAt = id, "open", 1, actorID, now, now
		saved = input
		return nil
	})
	return saved, err
}

func (s *Store) TransitionDefect(ctx context.Context, actorID, defectID, version int64, target, note, requestID string) (model.Defect, error) {
	now := s.now()
	var saved model.Defect
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := loadDefect(ctx, tx, defectID)
		if err != nil {
			return err
		}
		allowed := map[string]string{"open": "contained", "contained": "verified", "verified": "closed"}
		if allowed[current.Status] != target {
			return fault.New(fault.Conflict, "invalid_defect_transition", "defect action does not follow containment and verification order")
		}
		result, err := tx.ExecContext(ctx, `UPDATE defects SET status=?,version=version+1,updated_at=? WHERE id=? AND version=? AND status=?`, target, formatTime(now), defectID, version, current.Status)
		if err != nil {
			return fmt.Errorf("transition defect: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "stale_defect", "defect was changed by another operator")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO defect_actions(defect_id,actor_id,action,note,created_at) VALUES(?,?,?,?,?)`, defectID, actorID, target, note, formatTime(now)); err != nil {
			return fmt.Errorf("record defect action: %w", err)
		}
		if err = audit.New().Write(ctx, tx, actorID, "defect", defectID, target, "success", requestID, note); err != nil {
			return err
		}
		saved, err = loadDefect(ctx, tx, defectID)
		return err
	})
	return saved, err
}

func loadDefect(ctx context.Context, exec executor, id int64) (model.Defect, error) {
	var item model.Defect
	var leg sql.NullInt64
	var created, updated string
	err := exec.QueryRowContext(ctx, `SELECT id,vessel_id,leg_id,code,severity,title,description,status,version,created_by,created_at,updated_at FROM defects WHERE id=?`, id).Scan(&item.ID, &item.VesselID, &leg, &item.Code, &item.Severity, &item.Title, &item.Description, &item.Status, &item.Version, &item.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Defect{}, fault.New(fault.NotFound, "defect_not_found", "defect not found")
	}
	if err != nil {
		return model.Defect{}, fmt.Errorf("load defect: %w", err)
	}
	item.LegID = nullableInt(leg)
	if item.CreatedAt, err = parseTime(created); err != nil {
		return model.Defect{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return model.Defect{}, err
	}
	return item, nil
}

func (s *Store) Defect(ctx context.Context, id int64) (model.Defect, error) {
	return loadDefect(ctx, s.db, id)
}

func (s *Store) ListDefects(ctx context.Context, vesselID int64, status, severity string, limit, offset int) ([]model.Defect, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := `SELECT id,vessel_id,leg_id,code,severity,title,description,status,version,created_by,created_at,updated_at FROM defects WHERE vessel_id=?`
	args := []any{vesselID}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	if severity != "" {
		query += " AND severity=?"
		args = append(args, severity)
	}
	query += " ORDER BY CASE severity WHEN 'critical' THEN 1 WHEN 'major' THEN 2 ELSE 3 END,created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list defects: %w", err)
	}
	defer rows.Close()
	result := make([]model.Defect, 0)
	for rows.Next() {
		var item model.Defect
		var leg sql.NullInt64
		var created, updated string
		if err := rows.Scan(&item.ID, &item.VesselID, &leg, &item.Code, &item.Severity, &item.Title, &item.Description, &item.Status, &item.Version, &item.CreatedBy, &created, &updated); err != nil {
			return nil, err
		}
		item.LegID = nullableInt(leg)
		item.CreatedAt, _ = parseTime(created)
		item.UpdatedAt, _ = parseTime(updated)
		result = append(result, item)
	}
	return result, rows.Err()
}
