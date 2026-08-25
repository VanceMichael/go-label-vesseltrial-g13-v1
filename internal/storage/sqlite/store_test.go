package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

var fixedNow = time.Date(2026, 8, 25, 8, 30, 0, 0, time.UTC)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "vesseltrial.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	store.SetClock(func() time.Time { return fixedNow })
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func createUser(t *testing.T, store *Store, email string, role model.Role) model.User {
	t.Helper()
	user, err := store.CreateUser(context.Background(), email, email, role, "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func createVessel(t *testing.T, store *Store, suffix string) model.Vessel {
	t.Helper()
	vessel, err := store.CreateVessel(context.Background(), model.Vessel{
		Name:            "Beigang Yunhe " + suffix,
		CCSNumber:       "CCS-" + suffix,
		Owner:           "Beibu Gulf Port",
		BatteryCapacity: 4200,
	})
	if err != nil {
		t.Fatalf("create vessel: %v", err)
	}
	return vessel
}

func approvedReview(t *testing.T, store *Store, vessel model.Vessel, coordinator, surveyor model.User) model.ClassReview {
	t.Helper()
	review, err := store.CreateReview(context.Background(), coordinator.ID, vessel.ID, "CCS battery and propulsion dossier", "req-draft")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	review, err = store.SubmitReview(context.Background(), coordinator.ID, review.ID, review.Version, "submit-1", "req-submit")
	if err != nil {
		t.Fatalf("submit review: %v", err)
	}
	review, err = store.DecideReview(context.Background(), surveyor.ID, review.ID, review.Version, "approved", "documents and inspection accepted", "req-decision")
	if err != nil {
		t.Fatalf("approve review: %v", err)
	}
	return review
}

func scheduledPlan(t *testing.T, store *Store, vessel model.Vessel, review model.ClassReview, coordinator model.User, starts time.Time) (model.TrialPlan, model.VoyageLeg) {
	t.Helper()
	plan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, "Pinglu canal acceptance run", "Asia/Shanghai", "req-plan")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	leg, err := store.AddLeg(context.Background(), coordinator.ID, plan.ID, model.VoyageLeg{
		Name:     "Qinzhou inland section",
		Channel:  "Pinglu-01",
		StartsAt: starts,
		EndsAt:   starts.Add(90 * time.Minute),
	}, "req-leg")
	if err != nil {
		t.Fatalf("add leg: %v", err)
	}
	plan, err = store.SchedulePlan(context.Background(), coordinator.ID, plan.ID, plan.Version, "req-schedule")
	if err != nil {
		t.Fatalf("schedule plan: %v", err)
	}
	return plan, leg
}

func TestMigrateCreatesAllRelatedTablesAndIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	want := []string{
		"schema_migrations",
		"users",
		"sessions",
		"vessels",
		"class_reviews",
		"review_decisions",
		"trial_plans",
		"voyage_legs",
		"shore_stations",
		"telemetry_samples",
		"defects",
		"defect_actions",
		"delivery_batches",
		"worker_jobs",
		"idempotency_keys",
		"audit_events",
	}
	rows, err := store.db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		have[name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("required table %q was not created", name)
		}
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var versions int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&versions); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if versions != len(migrations) {
		t.Fatalf("migration ledger count = %d, want %d", versions, len(migrations))
	}
}

func TestMigrateRejectsConflictingHistory(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, "UPDATE schema_migrations SET name='tampered' WHERE version=1"); err != nil {
		t.Fatalf("tamper ledger: %v", err)
	}
	err := store.Migrate(ctx)
	if err == nil || !stringsContains(err.Error(), "history conflict") {
		t.Fatalf("Migrate error = %v, want history conflict", err)
	}
}

func TestOpenPreservesDataAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	ctx := context.Background()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	first.SetClock(func() time.Time { return fixedNow })
	vessel, err := first.CreateVessel(ctx, model.Vessel{Name: "Beigang Yunhe 001", CCSNumber: "CCS-R", Owner: "Beibu Gulf Port", BatteryCapacity: 4200})
	if err != nil {
		t.Fatalf("create vessel: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()
	got, err := second.Vessel(ctx, vessel.ID)
	if err != nil {
		t.Fatalf("load vessel after restart: %v", err)
	}
	if got.CCSNumber != vessel.CCSNumber || got.BatteryCapacity != 4200 {
		t.Fatalf("recovered vessel = %#v, want %#v", got, vessel)
	}
}

func TestSessionLifecycleIncludesExpiryAndRevocation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	user := createUser(t, store, "shore@example.test", model.RoleShore)
	session, err := store.CreateSession(ctx, user.ID, "token-hash-1", fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	resolved, loaded, err := store.ResolveSession(ctx, session.TokenHash, fixedNow)
	if err != nil {
		t.Fatalf("resolve active session: %v", err)
	}
	if resolved.ID != user.ID || loaded.ID != session.ID {
		t.Fatalf("resolved user/session = %d/%d, want %d/%d", resolved.ID, loaded.ID, user.ID, session.ID)
	}
	if err := store.RevokeSession(ctx, session.TokenHash, fixedNow.Add(time.Minute)); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, _, err := store.ResolveSession(ctx, session.TokenHash, fixedNow.Add(2*time.Minute)); !fault.IsKind(err, fault.Unauthorized) {
		t.Fatalf("revoked ResolveSession error = %v, want unauthorized", err)
	}
	expired, err := store.CreateSession(ctx, user.ID, "token-hash-2", fixedNow.Add(-time.Second))
	if err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	if _, _, err := store.ResolveSession(ctx, expired.TokenHash, fixedNow); !fault.IsKind(err, fault.Unauthorized) {
		t.Fatalf("expired ResolveSession error = %v, want unauthorized", err)
	}
}

func TestCreateReviewWritesAuditAtomically(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	vessel := createVessel(t, store, "A")
	review, err := store.CreateReview(context.Background(), coordinator.ID, vessel.ID, "initial CCS dossier", "request-create")
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	if review.Status != "draft" || review.Version != 1 {
		t.Fatalf("review state = %s/%d, want draft/1", review.Status, review.Version)
	}
	var count int
	err = store.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE object_type='class_review' AND object_id=? AND request_id='request-create'`, review.ID).Scan(&count)
	if err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit count = %d, want 1", count)
	}
}

func TestCreateReviewRollsBackWhenAuditCannotBeWritten(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	vessel := createVessel(t, store, "B")
	if _, err := store.db.Exec(`CREATE TRIGGER fail_review_audit BEFORE INSERT ON audit_events
WHEN NEW.object_type='class_review' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	_, err := store.CreateReview(context.Background(), coordinator.ID, vessel.ID, "must rollback", "request-fail")
	if err == nil {
		t.Fatal("CreateReview unexpectedly succeeded")
	}
	var reviews int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM class_reviews WHERE vessel_id=?", vessel.ID).Scan(&reviews); err != nil {
		t.Fatalf("count reviews: %v", err)
	}
	if reviews != 0 {
		t.Fatalf("review count after rollback = %d, want 0", reviews)
	}
}

func TestReviewIdempotencyReturnsSameSubmittedResource(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	vessel := createVessel(t, store, "C")
	review, err := store.CreateReview(context.Background(), coordinator.ID, vessel.ID, "submission dossier", "request-draft")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	first, err := store.SubmitReview(context.Background(), coordinator.ID, review.ID, review.Version, "same-key", "request-one")
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	second, err := store.SubmitReview(context.Background(), coordinator.ID, review.ID, review.Version, "same-key", "request-two")
	if err != nil {
		t.Fatalf("idempotent submit: %v", err)
	}
	if first.ID != second.ID || first.Version != second.Version || second.Status != "submitted" {
		t.Fatalf("idempotent result mismatch: first=%#v second=%#v", first, second)
	}
	var auditCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE object_type='class_review' AND object_id=? AND action='submit'", review.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count submit audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("submit audit count = %d, want 1", auditCount)
	}
}

func TestReviewDecisionRejectsStaleVersion(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "D")
	review, err := store.CreateReview(context.Background(), coordinator.ID, vessel.ID, "decision dossier", "request-draft")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	review, err = store.SubmitReview(context.Background(), coordinator.ID, review.ID, review.Version, "submit-d", "request-submit")
	if err != nil {
		t.Fatalf("submit review: %v", err)
	}
	approved, err := store.DecideReview(context.Background(), surveyor.ID, review.ID, review.Version, "approved", "accepted", "request-approve")
	if err != nil {
		t.Fatalf("approve review: %v", err)
	}
	if approved.Status != "approved" || approved.Version != review.Version+1 {
		t.Fatalf("approved review = %#v", approved)
	}
	_, err = store.DecideReview(context.Background(), surveyor.ID, review.ID, review.Version, "rejected", "stale overwrite", "request-stale")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("stale decision error = %v, want conflict", err)
	}
	loaded, err := store.Review(context.Background(), review.ID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if loaded.Status != "approved" {
		t.Fatalf("stale decision changed status to %q", loaded.Status)
	}
}

func TestCreatePlanRequiresApprovedReviewForSameVessel(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	vesselA := createVessel(t, store, "E1")
	vesselB := createVessel(t, store, "E2")
	review, err := store.CreateReview(context.Background(), coordinator.ID, vesselA.ID, "draft only", "request-review")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	_, err = store.CreatePlan(context.Background(), coordinator.ID, vesselA.ID, review.ID, "premature plan", "Asia/Shanghai", "request-plan")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("plan from draft review error = %v, want conflict", err)
	}
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	review, err = store.SubmitReview(context.Background(), coordinator.ID, review.ID, review.Version, "submit-e", "request-submit")
	if err != nil {
		t.Fatalf("submit review: %v", err)
	}
	review, err = store.DecideReview(context.Background(), surveyor.ID, review.ID, review.Version, "approved", "accepted", "request-approve")
	if err != nil {
		t.Fatalf("approve review: %v", err)
	}
	_, err = store.CreatePlan(context.Background(), coordinator.ID, vesselB.ID, review.ID, "wrong vessel", "Asia/Shanghai", "request-wrong")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("cross-vessel plan error = %v, want conflict", err)
	}
}

func TestPlanCannotScheduleWithoutLegs(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "F")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	plan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, "empty plan", "Asia/Shanghai", "request-plan")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, err = store.SchedulePlan(context.Background(), coordinator.ID, plan.ID, plan.Version, "request-schedule")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("empty schedule error = %v, want conflict", err)
	}
	loaded, err := store.Plan(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	if loaded.Status != "draft" || loaded.Version != 1 {
		t.Fatalf("failed schedule changed plan: %#v", loaded)
	}
}

func TestAddLegRejectsOverlappingWindowWithinPlan(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "G")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	plan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, "overlap plan", "Asia/Shanghai", "request-plan")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	start := fixedNow.Add(time.Hour)
	_, err = store.AddLeg(context.Background(), coordinator.ID, plan.ID, model.VoyageLeg{Name: "first", Channel: "A", StartsAt: start, EndsAt: start.Add(time.Hour)}, "request-first")
	if err != nil {
		t.Fatalf("add first leg: %v", err)
	}
	_, err = store.AddLeg(context.Background(), coordinator.ID, plan.ID, model.VoyageLeg{Name: "overlap", Channel: "B", StartsAt: start.Add(30 * time.Minute), EndsAt: start.Add(2 * time.Hour)}, "request-overlap")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("overlap error = %v, want conflict", err)
	}
}

func TestReleaseLegRejectsAnotherActiveWindow(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "H")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	start := fixedNow.Add(time.Hour)
	_, firstLeg := scheduledPlan(t, store, vessel, review, coordinator, start)
	firstLeg, err := store.ReleaseLeg(context.Background(), coordinator.ID, firstLeg.ID, firstLeg.Version, "request-release-one")
	if err != nil {
		t.Fatalf("release first leg: %v", err)
	}
	if firstLeg.Status != "released" {
		t.Fatalf("first leg status = %q", firstLeg.Status)
	}
	secondPlan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, "conflicting plan", "Asia/Shanghai", "request-plan-two")
	if err != nil {
		t.Fatalf("create second plan: %v", err)
	}
	secondLeg, err := store.AddLeg(context.Background(), coordinator.ID, secondPlan.ID, model.VoyageLeg{Name: "conflicting leg", Channel: "B", StartsAt: start.Add(10 * time.Minute), EndsAt: start.Add(40 * time.Minute)}, "request-leg-two")
	if err != nil {
		t.Fatalf("add second leg: %v", err)
	}
	secondPlan, err = store.SchedulePlan(context.Background(), coordinator.ID, secondPlan.ID, secondPlan.Version, "request-schedule-two")
	if err != nil {
		t.Fatalf("schedule second plan: %v", err)
	}
	_, err = store.ReleaseLeg(context.Background(), coordinator.ID, secondLeg.ID, secondLeg.Version, "request-release-two")
	if !fault.IsKind(err, fault.Conflict) {
		t.Fatalf("conflicting release error = %v, want conflict", err)
	}
	loaded, err := store.Leg(context.Background(), secondLeg.ID)
	if err != nil {
		t.Fatalf("load second leg: %v", err)
	}
	if loaded.Status != "planned" {
		t.Fatalf("failed release changed second leg to %q", loaded.Status)
	}
}

func TestConcurrentReleaseAllowsOnlyOneOverlappingLeg(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "I")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	start := fixedNow.Add(3 * time.Hour)
	_, first := scheduledPlan(t, store, vessel, review, coordinator, start)
	secondPlan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, "parallel plan", "Asia/Shanghai", "request-plan-two")
	if err != nil {
		t.Fatalf("create second plan: %v", err)
	}
	second, err := store.AddLeg(context.Background(), coordinator.ID, secondPlan.ID, model.VoyageLeg{Name: "parallel leg", Channel: "P2", StartsAt: start.Add(5 * time.Minute), EndsAt: start.Add(30 * time.Minute)}, "request-leg-two")
	if err != nil {
		t.Fatalf("add second leg: %v", err)
	}
	secondPlan, err = store.SchedulePlan(context.Background(), coordinator.ID, secondPlan.ID, secondPlan.Version, "request-schedule-two")
	if err != nil {
		t.Fatalf("schedule second plan: %v", err)
	}
	legs := []model.VoyageLeg{first, second}
	startGate := make(chan struct{})
	errorsFound := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range legs {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			_, releaseErr := store.ReleaseLeg(context.Background(), coordinator.ID, item.ID, item.Version, "parallel-release")
			errorsFound <- releaseErr
		}()
	}
	close(startGate)
	wg.Wait()
	close(errorsFound)
	var successes, conflicts int
	for releaseErr := range errorsFound {
		if releaseErr == nil {
			successes++
		} else if fault.IsKind(releaseErr, fault.Conflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected release error: %v", releaseErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("release outcomes successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
}

func TestCompletingLastLegCompletesPlan(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "J")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	plan, leg := scheduledPlan(t, store, vessel, review, coordinator, fixedNow.Add(time.Hour))
	leg, err := store.ReleaseLeg(context.Background(), coordinator.ID, leg.ID, leg.Version, "request-release")
	if err != nil {
		t.Fatalf("release leg: %v", err)
	}
	leg, err = store.CompleteLeg(context.Background(), coordinator.ID, leg.ID, leg.Version, "request-complete")
	if err != nil {
		t.Fatalf("complete leg: %v", err)
	}
	if leg.Status != "completed" {
		t.Fatalf("leg status = %q, want completed", leg.Status)
	}
	loaded, err := store.Plan(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	if loaded.Status != "completed" {
		t.Fatalf("plan status = %q, want completed", loaded.Status)
	}
}

func TestListLegsFiltersAndPaginatesInStartOrder(t *testing.T) {
	store := openTestStore(t)
	coordinator := createUser(t, store, "coordinator@example.test", model.RoleCoordinator)
	surveyor := createUser(t, store, "surveyor@example.test", model.RoleSurveyor)
	vessel := createVessel(t, store, "K")
	review := approvedReview(t, store, vessel, coordinator, surveyor)
	plan, err := store.CreatePlan(context.Background(), coordinator.ID, vessel.ID, review.ID, "query plan", "Asia/Shanghai", "request-plan")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	for index := 0; index < 4; index++ {
		start := fixedNow.Add(time.Duration(index+1) * 2 * time.Hour)
		_, err := store.AddLeg(context.Background(), coordinator.ID, plan.ID, model.VoyageLeg{Name: "leg-" + strconv.Itoa(index), Channel: "Q", StartsAt: start, EndsAt: start.Add(time.Hour)}, "request-leg")
		if err != nil {
			t.Fatalf("add leg %d: %v", index, err)
		}
	}
	page, err := store.ListLegs(context.Background(), vessel.ID, "planned", 2, 1)
	if err != nil {
		t.Fatalf("list legs: %v", err)
	}
	if len(page) != 2 || page[0].Name != "leg-1" || page[1].Name != "leg-2" {
		t.Fatalf("page = %#v, want leg-1 and leg-2", page)
	}
}

func TestForeignKeysRejectOrphanSession(t *testing.T) {
	store := openTestStore(t)
	_, err := store.db.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at,created_at) VALUES(9999,'orphan',?,?)`, formatTime(fixedNow.Add(time.Hour)), formatTime(fixedNow))
	if err == nil {
		t.Fatal("orphan session unexpectedly inserted")
	}
}

func TestInTxRollsBackReturnedError(t *testing.T) {
	store := openTestStore(t)
	wantErr := errors.New("stop transaction")
	err := store.InTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO shore_stations(code,name,status) VALUES('ROLLBACK','Rollback Station','offline')`)
		if err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("InTx error = %v, want %v", err, wantErr)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM shore_stations WHERE code='ROLLBACK'`).Scan(&count); err != nil {
		t.Fatalf("count stations: %v", err)
	}
	if count != 0 {
		t.Fatalf("rollback station count = %d, want 0", count)
	}
}

func stringsContains(value, part string) bool {
	return len(part) == 0 || (len(value) >= len(part) && strings.Index(value, part) >= 0)
}
