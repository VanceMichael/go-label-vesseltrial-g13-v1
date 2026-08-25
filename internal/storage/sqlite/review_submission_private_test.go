package sqlite

import (
	"context"
	"testing"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func TestReviewSubmissionFailureRollsBackStateIdempotencyAndAudit(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	coordinator := createUser(t, store, "rollback-coordinator@example.test", model.RoleCoordinator)
	vessel := createVessel(t, store, "SUBMIT-ROLLBACK")
	review, err := store.CreateReview(ctx, coordinator.ID, vessel.ID, "battery safety dossier", "review-draft")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_submission_audit BEFORE INSERT ON audit_events
WHEN NEW.object_type='class_review' AND NEW.action='submit' BEGIN SELECT RAISE(ABORT, 'submission audit unavailable'); END`); err != nil {
		t.Fatalf("create audit failure trigger: %v", err)
	}

	const idempotencyKey = "review-submit-rollback"
	if _, err := store.SubmitReview(ctx, coordinator.ID, review.ID, review.Version, idempotencyKey, "review-submit-failed"); err == nil {
		t.Fatal("SubmitReview unexpectedly succeeded while its audit write was failing")
	}

	afterFailure, err := store.Review(ctx, review.ID)
	if err != nil {
		t.Fatalf("load review after failed submission: %v", err)
	}
	if afterFailure.Status != "draft" || afterFailure.Version != review.Version {
		t.Errorf("failed submission left review at %s/version %d, want draft/version %d", afterFailure.Status, afterFailure.Version, review.Version)
	}
	var idempotencyRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM idempotency_keys
WHERE actor_id=? AND method='POST' AND route='/reviews/submit' AND idem_key=?`, coordinator.ID, idempotencyKey).Scan(&idempotencyRows); err != nil {
		t.Fatalf("count idempotency rows: %v", err)
	}
	if idempotencyRows != 0 {
		t.Errorf("failed submission left %d idempotency rows, want 0", idempotencyRows)
	}
	var auditRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM audit_events
WHERE object_type='class_review' AND object_id=? AND action='submit'`, review.ID).Scan(&auditRows); err != nil {
		t.Fatalf("count submission audits: %v", err)
	}
	if auditRows != 0 {
		t.Errorf("failed submission left %d submission audits, want 0", auditRows)
	}

	if _, err := store.db.Exec(`DROP TRIGGER fail_submission_audit`); err != nil {
		t.Fatalf("remove audit failure trigger: %v", err)
	}
	submitted, err := store.SubmitReview(ctx, coordinator.ID, review.ID, review.Version, idempotencyKey, "review-submit-retry")
	if err != nil {
		t.Fatalf("retry submission with original idempotency key: %v", err)
	}
	if submitted.Status != "submitted" || submitted.Version != review.Version+1 {
		t.Errorf("retry result = %s/version %d, want submitted/version %d", submitted.Status, submitted.Version, review.Version+1)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM idempotency_keys
WHERE actor_id=? AND method='POST' AND route='/reviews/submit' AND idem_key=?`, coordinator.ID, idempotencyKey).Scan(&idempotencyRows); err != nil {
		t.Fatalf("count idempotency rows after retry: %v", err)
	}
	if idempotencyRows != 1 {
		t.Errorf("successful retry idempotency rows = %d, want 1", idempotencyRows)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM audit_events
WHERE object_type='class_review' AND object_id=? AND action='submit'`, review.ID).Scan(&auditRows); err != nil {
		t.Fatalf("count submission audits after retry: %v", err)
	}
	if auditRows != 1 {
		t.Errorf("successful retry submission audits = %d, want 1", auditRows)
	}
}
