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

type overlappingLegBarrier struct {
	trialservice.Repository
	mu       sync.Mutex
	ready    int
	allReady chan struct{}
}

func newOverlappingLegBarrier(repository trialservice.Repository) *overlappingLegBarrier {
	return &overlappingLegBarrier{Repository: repository, allReady: make(chan struct{})}
}

func (b *overlappingLegBarrier) HasOverlappingLeg(ctx context.Context, planID int64, leg model.VoyageLeg) (bool, error) {
	overlaps, err := b.Repository.HasOverlappingLeg(ctx, planID, leg)
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

func TestConcurrentLegCreationPreservesPlanWindowExclusivity(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator-private@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor-private@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "private-leg")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	plan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, "private overlap plan", "Asia/Shanghai", "private-plan")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	repository := newOverlappingLegBarrier(store)
	service := trialservice.New(repository)
	start := fixedNow.Add(2 * time.Hour)
	legs := []model.VoyageLeg{
		{Name: "north channel", Channel: "N-01", StartsAt: start, EndsAt: start.Add(time.Hour)},
		{Name: "south channel", Channel: "S-02", StartsAt: start.Add(30 * time.Minute), EndsAt: start.Add(90 * time.Minute)},
	}
	results := make(chan error, len(legs))
	var wg sync.WaitGroup
	for i := range legs {
		leg := legs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, callErr := service.AddLeg(context.Background(), coordinator, plan.ID, leg, "private-leg-"+leg.Name)
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
			t.Fatalf("unexpected concurrent overlapping leg error: %v", callErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent overlapping leg outcomes successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}

	created, err := store.ListLegs(context.Background(), vessel.ID, "", 50, 0)
	if err != nil {
		t.Fatalf("list created legs: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created legs = %d, want exactly one owner for the overlapping window", len(created))
	}

	_, err = service.AddLeg(context.Background(), coordinator, plan.ID, model.VoyageLeg{
		Name: "evening channel", Channel: "E-03", StartsAt: start.Add(3 * time.Hour), EndsAt: start.Add(4 * time.Hour),
	}, "private-leg-non-overlap")
	if err != nil {
		t.Fatalf("non-overlapping leg should remain creatable: %v", err)
	}
}
