package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpiredWorkerCannotCompleteReclaimedJob(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := fixedNow
	store.SetClock(func() time.Time { return now })

	job, err := store.EnqueueJob(ctx, "telemetry_staleness_check", `{"max_age_seconds":60}`, 3, now)
	if err != nil {
		t.Fatalf("enqueue completion job: %v", err)
	}
	first, err := store.ClaimJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	now = now.Add(2 * time.Minute)
	second, err := store.ClaimJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("reclaim after lease expiry: %v", err)
	}
	if second.ID != job.ID || second.ClaimToken == first.ClaimToken {
		t.Fatalf("reclaimed lease = %#v, first token=%q", second, first.ClaimToken)
	}
	if err := store.CompleteJobWithToken(ctx, job.ID, first.ClaimToken); err == nil {
		t.Fatal("expired worker completed the reclaimed job")
	}
	loaded, err := store.Job(ctx, job.ID)
	if err != nil {
		t.Fatalf("load reclaimed completion job: %v", err)
	}
	if loaded.Status != "running" || loaded.LockedUntil == nil || !loaded.LockedUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("stale completion changed new lease: %#v", loaded)
	}

	failJob, err := store.EnqueueJob(ctx, "telemetry_staleness_check", `{"max_age_seconds":60}`, 3, now)
	if err != nil {
		t.Fatalf("enqueue failure job: %v", err)
	}
	old, err := store.ClaimJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("failure job first claim: %v", err)
	}
	now = now.Add(2 * time.Minute)
	newOwner, err := store.ClaimJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("failure job reclaim: %v", err)
	}
	if newOwner.ID != failJob.ID || newOwner.ClaimToken == old.ClaimToken {
		t.Fatalf("failure job lease generations = %q/%q", old.ClaimToken, newOwner.ClaimToken)
	}
	if err := store.FailJobWithToken(ctx, failJob.ID, old.ClaimToken, errors.New("late failure"), time.Second); err == nil {
		t.Fatal("expired worker failed the reclaimed job")
	}
	loaded, err = store.Job(ctx, failJob.ID)
	if err != nil {
		t.Fatalf("load reclaimed failure job: %v", err)
	}
	if loaded.Status != "running" || loaded.Attempts != newOwner.Attempts {
		t.Fatalf("stale failure changed new lease: %#v", loaded)
	}
}
