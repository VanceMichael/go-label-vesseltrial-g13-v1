package sqlite

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
	trialservice "github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/trial"
)

type releaseWindowBarrier struct {
	trialservice.Repository
	mu       sync.Mutex
	ready    int
	allReady chan struct{}
	writeMu  sync.Mutex
}

func newReleaseWindowBarrier(repository trialservice.Repository) *releaseWindowBarrier {
	return &releaseWindowBarrier{Repository: repository, allReady: make(chan struct{})}
}

func (b *releaseWindowBarrier) HasActiveWindow(ctx context.Context, legID int64) (bool, error) {
	overlaps, err := b.Repository.HasActiveWindow(ctx, legID)
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	b.ready++
	if b.ready == 2 {
		close(b.allReady)
	}
	b.mu.Unlock()
	<-b.allReady
	return overlaps, nil
}

func (b *releaseWindowBarrier) ReleaseLegUnchecked(ctx context.Context, actorID, legID, expectedVersion int64, requestID string) (model.VoyageLeg, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.Repository.ReleaseLegUnchecked(ctx, actorID, legID, expectedVersion, requestID)
}

func TestConcurrentCrossPlanReleaseKeepsSingleActiveWindow(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator-release-private@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor-release-private@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "release-private")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	start := fixedNow.Add(4 * time.Hour)
	createScheduledPlan := func(name string, begins time.Time) (model.TrialPlan, model.VoyageLeg) {
		t.Helper()
		plan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, name, "Asia/Shanghai", "private-"+name)
		if err != nil {
			t.Fatalf("create plan %q: %v", name, err)
		}
		leg, err := store.AddLeg(context.Background(), coordinator.ID, plan.ID, model.VoyageLeg{
			Name:     name + " leg",
			Channel:  "Pinglu-01",
			StartsAt: begins,
			EndsAt:   begins.Add(90 * time.Minute),
		}, "private-leg-"+name)
		if err != nil {
			t.Fatalf("add leg %q: %v", name, err)
		}
		plan, err = store.SchedulePlan(context.Background(), coordinator.ID, plan.ID, plan.Version, "private-schedule-"+name)
		if err != nil {
			t.Fatalf("schedule plan %q: %v", name, err)
		}
		return plan, leg
	}
	firstPlan, firstLeg := createScheduledPlan("release-window-first", start)
	secondPlan, secondLeg := createScheduledPlan("release-window-second", start.Add(20*time.Minute))
	if firstPlan.Status != "scheduled" || secondPlan.Status != "scheduled" {
		t.Fatalf("plans must be scheduled before release: %#v %#v", firstPlan, secondPlan)
	}

	repository := newReleaseWindowBarrier(store)
	service := trialservice.New(repository)
	legs := []model.VoyageLeg{firstLeg, secondLeg}
	results := make(chan error, len(legs))
	var wg sync.WaitGroup
	for _, leg := range legs {
		leg := leg
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, callErr := service.Release(context.Background(), coordinator, leg.ID, leg.Version, "private-release-"+leg.Name)
			results <- callErr
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for callErr := range results {
		if callErr == nil {
			successes++
		} else if fault.IsKind(callErr, fault.Conflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent release error: %v", callErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("release outcomes successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}

	created, err := store.ListLegs(context.Background(), vessel.ID, "released", 50, 0)
	if err != nil {
		t.Fatalf("list released legs: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("released legs = %d, want one active window owner", len(created))
	}
	first, err := store.Leg(context.Background(), firstLeg.ID)
	if err != nil {
		t.Fatalf("load first leg: %v", err)
	}
	second, err := store.Leg(context.Background(), secondLeg.ID)
	if err != nil {
		t.Fatalf("load second leg: %v", err)
	}
	activePlans := 0
	for _, planID := range []int64{first.PlanID, second.PlanID} {
		plan, planErr := store.Plan(context.Background(), planID)
		if planErr != nil {
			t.Fatalf("load plan %d: %v", planID, planErr)
		}
		if plan.Status == "active" {
			activePlans++
		}
	}
	if activePlans != 1 {
		t.Fatalf("active plans = %d, want one owner", activePlans)
	}

	// A non-overlapping scheduled plan must still be releasable.
	thirdPlan, thirdLeg := createScheduledPlan("release-window-third", start.Add(4*time.Hour))
	if _, err := service.Release(context.Background(), coordinator, thirdLeg.ID, thirdLeg.Version, "private-release-non-overlap"); err != nil {
		t.Fatalf("non-overlapping release for plan %d should succeed: %v", thirdPlan.ID, err)
	}
}
