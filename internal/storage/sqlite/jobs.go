package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func (s *Store) EnqueueJob(ctx context.Context, kind, payload string, maxAttempts int, availableAt time.Time) (model.WorkerJob, error) {
	now := s.now()
	result, err := s.db.ExecContext(ctx, `INSERT INTO worker_jobs(kind,payload,status,attempts,max_attempts,available_at,created_at,updated_at) VALUES(?,?,'pending',0,?,?,?,?)`, kind, payload, maxAttempts, formatTime(availableAt), formatTime(now), formatTime(now))
	if err != nil {
		return model.WorkerJob{}, fmt.Errorf("enqueue worker job: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.WorkerJob{}, err
	}
	return model.WorkerJob{ID: id, Kind: kind, Payload: payload, Status: "pending", MaxAttempts: maxAttempts, AvailableAt: availableAt}, nil
}

func (s *Store) ClaimJob(ctx context.Context, lease time.Duration) (model.WorkerJob, error) {
	now := s.now()
	lockedUntil := now.Add(lease)
	var claimed model.WorkerJob
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT id,kind,payload,status,attempts,max_attempts,available_at,locked_until,last_error FROM worker_jobs WHERE ((status IN ('pending','retry')) OR (status='running' AND (locked_until IS NULL OR locked_until<?))) AND available_at<=? ORDER BY available_at,id LIMIT 1`, formatTime(now), formatTime(now))
		item, err := scanJob(row)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE worker_jobs SET status='running',attempts=attempts+1,locked_until=?,updated_at=? WHERE id=? AND ((status IN ('pending','retry')) OR (status='running' AND (locked_until IS NULL OR locked_until<?)))`, formatTime(lockedUntil), formatTime(now), item.ID, formatTime(now))
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fault.New(fault.Conflict, "job_claim_conflict", "worker job was claimed concurrently")
		}
		item.Status = "running"
		item.Attempts++
		item.LockedUntil = &lockedUntil
		item.ClaimToken = formatTime(lockedUntil)
		claimed = item
		return nil
	})
	return claimed, err
}

func (s *Store) CompleteJob(ctx context.Context, id int64) error {
	return s.CompleteJobWithToken(ctx, id, "")
}

// CompleteJobWithToken completes a worker job with its claim metadata.
func (s *Store) CompleteJobWithToken(ctx context.Context, id int64, claimToken string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE worker_jobs SET status='succeeded',locked_until=NULL,updated_at=? WHERE id=? AND status='running'`, formatTime(s.now()), id)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fault.New(fault.Conflict, "job_not_running", "worker job is not running")
	}
	return nil
}

func (s *Store) RescheduleJob(ctx context.Context, id int64, availableAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE worker_jobs SET status='pending',attempts=0,available_at=?,locked_until=NULL,last_error='',updated_at=? WHERE id=? AND status='running'`, formatTime(availableAt), formatTime(s.now()), id)
	if err != nil {
		return fmt.Errorf("reschedule job: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fault.New(fault.Conflict, "job_not_running", "worker job is not running")
	}
	return nil
}

func (s *Store) EnsureJob(ctx context.Context, kind, payload string, maxAttempts int, availableAt time.Time) error {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worker_jobs WHERE kind=? AND status IN ('pending','running','retry')`, kind).Scan(&count)
	if err != nil {
		return fmt.Errorf("check active jobs: %w", err)
	}
	if count != 0 {
		return nil
	}
	_, err = s.EnqueueJob(ctx, kind, payload, maxAttempts, availableAt)
	return err
}

func (s *Store) FailJob(ctx context.Context, id int64, cause error, backoff time.Duration) error {
	return s.FailJobWithToken(ctx, id, "", cause, backoff)
}

// FailJobWithToken records a failed worker execution with its claim metadata.
func (s *Store) FailJobWithToken(ctx context.Context, id int64, claimToken string, cause error, backoff time.Duration) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		item, err := scanJob(tx.QueryRowContext(ctx, `SELECT id,kind,payload,status,attempts,max_attempts,available_at,locked_until,last_error FROM worker_jobs WHERE id=?`, id))
		if err != nil {
			return err
		}
		if item.Status != "running" {
			return fault.New(fault.Conflict, "job_not_running", "worker job is not running")
		}
		target := "retry"
		if item.Attempts >= item.MaxAttempts {
			target = "failed"
		}
		_, err = tx.ExecContext(ctx, `UPDATE worker_jobs SET status=?,available_at=?,locked_until=NULL,last_error=?,updated_at=? WHERE id=?`, target, formatTime(s.now().Add(backoff)), cause.Error(), formatTime(s.now()), id)
		return err
	})
}

func (s *Store) Job(ctx context.Context, id int64) (model.WorkerJob, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT id,kind,payload,status,attempts,max_attempts,available_at,locked_until,last_error FROM worker_jobs WHERE id=?`, id))
}
func scanJob(row *sql.Row) (model.WorkerJob, error) {
	var item model.WorkerJob
	var available string
	var locked sql.NullString
	err := row.Scan(&item.ID, &item.Kind, &item.Payload, &item.Status, &item.Attempts, &item.MaxAttempts, &available, &locked, &item.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return model.WorkerJob{}, fault.New(fault.NotFound, "job_not_found", "worker job not found")
	}
	if err != nil {
		return model.WorkerJob{}, fmt.Errorf("scan worker job: %w", err)
	}
	if item.AvailableAt, err = parseTime(available); err != nil {
		return model.WorkerJob{}, err
	}
	item.LockedUntil, err = nullableTime(locked)
	return item, err
}
