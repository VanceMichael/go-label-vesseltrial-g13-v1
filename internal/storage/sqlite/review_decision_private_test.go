package sqlite

import (
	"context"
	"sync"
	"testing"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
	reviewservice "github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/review"
)

type decisionReadBarrier struct {
	store   *Store
	mu      sync.Mutex
	seen    int
	release chan struct{}
}

func newDecisionReadBarrier(store *Store) *decisionReadBarrier {
	return &decisionReadBarrier{store: store, release: make(chan struct{})}
}

func (b *decisionReadBarrier) Review(ctx context.Context, id int64) (model.ClassReview, error) {
	got, err := b.store.Review(ctx, id)
	if err != nil {
		return model.ClassReview{}, err
	}
	b.mu.Lock()
	b.seen++
	if b.seen == 2 {
		close(b.release)
	}
	b.mu.Unlock()
	select {
	case <-b.release:
		return got, nil
	case <-ctx.Done():
		return model.ClassReview{}, ctx.Err()
	}
}

func (b *decisionReadBarrier) CreateVessel(ctx context.Context, vessel model.Vessel) (model.Vessel, error) {
	return b.store.CreateVessel(ctx, vessel)
}

func (b *decisionReadBarrier) CreateReview(ctx context.Context, actorID, vesselID int64, notes, requestID string) (model.ClassReview, error) {
	return b.store.CreateReview(ctx, actorID, vesselID, notes, requestID)
}

func (b *decisionReadBarrier) SubmitReview(ctx context.Context, actorID, reviewID, version int64, idemKey, requestID string) (model.ClassReview, error) {
	return b.store.SubmitReview(ctx, actorID, reviewID, version, idemKey, requestID)
}

func (b *decisionReadBarrier) DecideReview(ctx context.Context, reviewerID, reviewID, version int64, decision, reason, requestID string) (model.ClassReview, error) {
	return b.store.DecideReview(ctx, reviewerID, reviewID, version, decision, reason, requestID)
}

func TestConcurrentReviewDecisionHasSingleOwnerAndAudit(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator.concurrent@example.test", model.RoleCoordinator)
	surveyorA := createUser(t, store, "surveyor-a.concurrent@example.test", model.RoleSurveyor)
	surveyorB := createUser(t, store, "surveyor-b.concurrent@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "CONCURRENT")
	review, err := store.CreateReview(context.Background(), coordinator.ID, vessel.ID, "concurrent decision dossier", "request-draft-concurrent")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	review, err = store.SubmitReview(context.Background(), coordinator.ID, review.ID, review.Version, "submit-concurrent", "request-submit-concurrent")
	if err != nil {
		t.Fatalf("submit review: %v", err)
	}

	barrier := newDecisionReadBarrier(store)
	service := reviewservice.New(barrier)
	start := make(chan struct{})
	type result struct {
		status string
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, input := range []struct {
		actor    model.User
		decision string
		request  string
	}{
		{surveyorA, "approved", "request-approved-concurrent"},
		{surveyorB, "rejected", "request-rejected-concurrent"},
	} {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := service.Decide(context.Background(), input.actor, review.ID, review.Version, input.decision, input.decision+" after evidence review", input.request)
			results <- result{status: got.Status, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for got := range results {
		if got.err == nil {
			successes++
			if got.status != "approved" && got.status != "rejected" {
				t.Fatalf("successful decision returned status %q", got.status)
			}
			continue
		}
		if fault.IsKind(got.err, fault.Conflict) {
			conflicts++
			continue
		}
		t.Fatalf("concurrent decision error = %v, want conflict for the loser", got.err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent decisions successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}

	loaded, err := store.Review(context.Background(), review.ID)
	if err != nil {
		t.Fatalf("load final review: %v", err)
	}
	if loaded.Version != review.Version+1 || (loaded.Status != "approved" && loaded.Status != "rejected") {
		t.Fatalf("final review = %#v, want one versioned decision", loaded)
	}
	var decisions, audits int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM review_decisions WHERE review_id=?", review.ID).Scan(&decisions); err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE object_type='class_review' AND object_id=? AND action='decide'", review.ID).Scan(&audits); err != nil {
		t.Fatalf("count decision audits: %v", err)
	}
	if decisions != 1 || audits != 1 {
		t.Fatalf("decision history decisions=%d audits=%d, want 1/1 owned by the winner", decisions, audits)
	}
}
