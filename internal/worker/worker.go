package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

type Repository interface {
	ClaimJob(context.Context, time.Duration) (model.WorkerJob, error)
	CompleteJob(context.Context, int64) error
	RescheduleJob(context.Context, int64, time.Time) error
	FailJob(context.Context, int64, error, time.Duration) error
	MarkStaleStations(context.Context, time.Time) (int64, error)
}
type Worker struct {
	repository                  Repository
	logger                      *slog.Logger
	interval, lease, maxBackoff time.Duration
	now                         func() time.Time
}

func New(repository Repository, logger *slog.Logger, interval time.Duration) *Worker {
	return &Worker{repository: repository, logger: logger, interval: interval, lease: 30 * time.Second, maxBackoff: time.Minute, now: func() time.Time { return time.Now().UTC() }}
}
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("worker stopped", "reason", ctx.Err())
			return
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil && !fault.IsKind(err, fault.NotFound) {
				w.logger.Error("worker cycle failed", "error", err)
			}
		}
	}
}
func (w *Worker) RunOnce(ctx context.Context) error {
	job, err := w.repository.ClaimJob(ctx, w.lease)
	if err != nil {
		return err
	}
	runErr := w.execute(ctx, job)
	if runErr == nil {
		if job.Kind == "telemetry_staleness_check" {
			return w.repository.RescheduleJob(ctx, job.ID, w.now().Add(w.interval))
		}
		return w.repository.CompleteJob(ctx, job.ID)
	}
	shift := time.Second << min(job.Attempts-1, 6)
	if shift > w.maxBackoff {
		shift = w.maxBackoff
	}
	if failErr := w.repository.FailJob(ctx, job.ID, runErr, shift); failErr != nil {
		return fmt.Errorf("job failed (%v), persistence failed: %w", runErr, failErr)
	}
	return runErr
}
func (w *Worker) execute(ctx context.Context, job model.WorkerJob) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("worker canceled before job %d: %w", job.ID, err)
	}
	switch job.Kind {
	case "telemetry_staleness_check":
		var payload struct {
			MaxAgeSeconds int64 `json:"max_age_seconds"`
		}
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return fmt.Errorf("decode freshness job: %w", err)
		}
		if payload.MaxAgeSeconds <= 0 {
			return errors.New("max_age_seconds must be positive")
		}
		_, err := w.repository.MarkStaleStations(ctx, w.now().Add(-time.Duration(payload.MaxAgeSeconds)*time.Second))
		return err
	case "delivery_gate_recheck":
		return nil
	default:
		return fmt.Errorf("unsupported job kind %q", job.Kind)
	}
}
