package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/storage/sqlite"
)

type cancelAfterFreshnessRepo struct {
	*sqlite.Store
	cancel context.CancelFunc
}

func (r *cancelAfterFreshnessRepo) MarkStaleStations(ctx context.Context, cutoff time.Time) (int64, error) {
	changed, err := r.Store.MarkStaleStations(ctx, cutoff)
	if err == nil {
		r.cancel()
	}
	return changed, err
}

func TestShutdownAfterFreshnessUpdateReleasesPeriodicJob(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/worker.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	fixedNow := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return fixedNow })

	station, err := store.CreateStation(ctx, "QZ-SHUTDOWN", "Shutdown Test Station")
	if err != nil {
		t.Fatalf("create station: %v", err)
	}
	oldSeen := fixedNow.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	if _, err := store.DB().ExecContext(ctx, "UPDATE shore_stations SET status='online', last_seen_at=? WHERE id=?", oldSeen, station.ID); err != nil {
		t.Fatalf("seed station freshness: %v", err)
	}
	job, err := store.EnqueueJob(ctx, "telemetry_staleness_check", `{"max_age_seconds":60}`, 3, fixedNow)
	if err != nil {
		t.Fatalf("enqueue freshness job: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	repo := &cancelAfterFreshnessRepo{Store: store, cancel: cancel}
	w := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Minute)
	if err := w.RunOnce(runCtx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce error = %v, want cancellation after freshness update", err)
	}

	loadedStation, err := store.Station(ctx, station.ID)
	if err != nil {
		t.Fatalf("load station: %v", err)
	}
	if loadedStation.Status != "stale" {
		t.Fatalf("station status = %q, want stale", loadedStation.Status)
	}
	loadedJob, err := store.Job(ctx, job.ID)
	if err != nil {
		t.Fatalf("load worker job: %v", err)
	}
	if loadedJob.Status != "pending" || loadedJob.LockedUntil != nil || loadedJob.Attempts != 0 {
		t.Fatalf("worker job after canceled shutdown = %#v, want pending lease released", loadedJob)
	}
	if !loadedJob.AvailableAt.Equal(fixedNow.Add(time.Minute)) {
		t.Fatalf("worker job available_at = %v, want next interval %v", loadedJob.AvailableAt, fixedNow.Add(time.Minute))
	}
}
