package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Writer struct{ now func() time.Time }

func New() Writer { return Writer{now: func() time.Time { return time.Now().UTC() }} }

func (w Writer) Write(ctx context.Context, tx *sql.Tx, actorID int64, objectType string, objectID int64, action, outcome, requestID, details string) error {
	if actorID <= 0 || objectID <= 0 || action == "" || requestID == "" {
		return fmt.Errorf("audit identity, object, action and request id are required")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,object_type,object_id,action,outcome,request_id,details,created_at)
VALUES(?,?,?,?,?,?,?,?)`, actorID, objectType, objectID, action, outcome, requestID, details, w.now().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

func (w Writer) WriteDB(ctx context.Context, db *sql.DB, actorID int64, objectType string, objectID int64, action, outcome, requestID, details string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit transaction: %w", err)
	}
	if err := w.Write(ctx, tx, actorID, objectType, objectID, action, outcome, requestID, details); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit audit transaction: %w", err)
	}
	return nil
}
