package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/experimental/store/postgres"
	"github.com/deepnoodle-ai/workflow/experimental/worker"
	"github.com/deepnoodle-ai/workflow/experimental/worker/runquery"
)

// These tests require a real Postgres instance. Set WORKFLOW_PG_DSN
// to opt in; without it, they are skipped. A throwaway database is
// recommended — Migrate creates tables and tests truncate them.
//
// Example:
//
//	WORKFLOW_PG_DSN="postgres://postgres@localhost:5432/workflow_test?sslmode=disable" \
//	    go test ./postgres/...
const dsnEnv = "WORKFLOW_PG_DSN"

func openTestStore(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("set %s to run postgres store tests", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	store := postgres.New(pool)
	if err := store.Migrate(ctx); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	// Clean state between tests.
	for _, tbl := range []string{
		"workflow_activity_log",
		"workflow_step_progress",
		"workflow_credit_ledger",
		"workflow_triggers",
		"workflow_webhooks",
		"workflow_events",
		"workflow_runs",
	} {
		if _, err := pool.Exec(ctx, "TRUNCATE "+tbl); err != nil {
			pool.Close()
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	t.Cleanup(func() { pool.Close() })
	return store, pool
}

func TestStore_EnqueueClaimCompleteRoundTrip(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	if err := store.Enqueue(ctx, worker.NewRun{
		ID:   "run-a",
		Spec: []byte(`{"type":"demo"}`),
	}); err != nil {
		t.Fatal(err)
	}

	claim, err := store.ClaimQueued(ctx, "test-worker")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v / %+v", err, claim)
	}
	if claim.ID != "run-a" || claim.Attempt != 1 || string(claim.Spec) != `{"type":"demo"}` {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	if claim.WorkerID != "test-worker" {
		t.Fatalf("claim.WorkerID = %q, want %q", claim.WorkerID, "test-worker")
	}

	again, err := store.ClaimQueued(ctx, "test-worker")
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("expected no more queued runs, got %+v", again)
	}

	if err := store.Heartbeat(ctx, claim); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	if err := store.Complete(ctx, claim, worker.Outcome{
		Status: worker.StatusCompleted,
		Result: []byte(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestStore_HeartbeatLeaseLost(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	if err := store.Enqueue(ctx, worker.NewRun{ID: "run-b", Spec: []byte(`{}`)}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, err := store.ClaimQueued(ctx, "alpha")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v / %+v", err, claim)
	}

	// Wrong worker ID should be rejected.
	wrong := *claim
	wrong.WorkerID = "beta"
	if err := store.Heartbeat(ctx, &wrong); err != worker.ErrLeaseLost {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}
	// Wrong attempt should be rejected.
	badAttempt := *claim
	badAttempt.Attempt = 99
	if err := store.Heartbeat(ctx, &badAttempt); err != worker.ErrLeaseLost {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}
}

func TestStore_ReclaimAndDeadLetter(t *testing.T) {
	store, pool := openTestStore(t)
	ctx := context.Background()

	_ = store.Enqueue(ctx, worker.NewRun{ID: "reclaim-me", Spec: []byte(`{}`)})
	_ = store.Enqueue(ctx, worker.NewRun{ID: "dead-letter-me", Spec: []byte(`{}`)})

	c1, _ := store.ClaimQueued(ctx, "w")
	c2, _ := store.ClaimQueued(ctx, "w")

	// Force dead-letter-me to the max attempt and both heartbeats stale.
	past := time.Now().Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `
		UPDATE workflow_runs
		SET heartbeat_at = $1,
		    attempt      = CASE WHEN id = $2 THEN 3 ELSE attempt END
		WHERE id IN ($2, $3)
	`, past, "dead-letter-me", "reclaim-me"); err != nil {
		t.Fatalf("force stale: %v", err)
	}
	_ = c1
	_ = c2

	staleBefore := time.Now().Add(-1 * time.Minute)

	dead, err := store.DeadLetterStale(ctx, staleBefore, 3, nil)
	if err != nil {
		t.Fatalf("dead letter: %v", err)
	}
	if len(dead) != 1 || dead[0].ID != "dead-letter-me" {
		t.Fatalf("expected [dead-letter-me], got %v", dead)
	}

	n, err := store.ReclaimStale(ctx, staleBefore, 3, nil)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reclaim, got %d", n)
	}
}

func TestStore_CheckpointerRoundTrip(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	_ = store.Enqueue(ctx, worker.NewRun{ID: "cp-run", Spec: []byte(`{}`)})
	claim, _ := store.ClaimQueued(ctx, "w")

	cp := store.NewCheckpointer(claim)

	// Load before save -> ErrNoCheckpoint.
	if _, err := cp.LoadCheckpoint(ctx, claim.ID); err != workflow.ErrNoCheckpoint {
		t.Fatalf("expected ErrNoCheckpoint, got %v", err)
	}

	// Save.
	original := &workflow.Checkpoint{
		ID:            "c1",
		ExecutionID:   claim.ID,
		SchemaVersion: workflow.CheckpointSchemaVersion,
		WorkflowName:  "demo",
	}
	if err := cp.SaveCheckpoint(ctx, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load.
	loaded, err := cp.LoadCheckpoint(ctx, claim.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.WorkflowName != "demo" {
		t.Fatalf("round-trip mismatch: %+v", loaded)
	}

	// Wrong lease -> ErrLeaseLost.
	otherWorker := *claim
	otherWorker.WorkerID = "someone-else"
	bogus := store.NewCheckpointer(&otherWorker)
	if err := bogus.SaveCheckpoint(ctx, original); err != worker.ErrLeaseLost {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}

	// Delete.
	if err := cp.DeleteCheckpoint(ctx, claim.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cp.LoadCheckpoint(ctx, claim.ID); err != workflow.ErrNoCheckpoint {
		t.Fatalf("expected ErrNoCheckpoint after delete, got %v", err)
	}
}

func TestStore_StepProgressAndActivityLog(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	_ = store.Enqueue(ctx, worker.NewRun{ID: "obs-run", Spec: []byte(`{}`)})

	// Step progress upsert.
	p := workflow.StepProgress{
		StepName:     "classify",
		BranchID:     "main",
		Status:       workflow.StepStatusRunning,
		ActivityName: "print",
		Attempt:      1,
		StartedAt:    time.Now(),
	}
	if err := store.UpdateStepProgress(ctx, "obs-run", p); err != nil {
		t.Fatalf("update progress: %v", err)
	}
	p.Status = workflow.StepStatusCompleted
	p.FinishedAt = time.Now()
	if err := store.UpdateStepProgress(ctx, "obs-run", p); err != nil {
		t.Fatalf("update progress 2: %v", err)
	}

	// Activity log.
	entry := &workflow.ActivityLogEntry{
		ID:          "log-1",
		ExecutionID: "obs-run",
		Activity:    "print",
		StepName:    "classify",
		BranchID:    "main",
		Parameters:  map[string]any{"message": "hi"},
		Result:      "done",
		StartTime:   time.Now(),
		Duration:    0.05,
	}
	if err := store.LogActivity(ctx, entry); err != nil {
		t.Fatalf("log activity: %v", err)
	}

	hist, err := store.GetActivityHistory(ctx, "obs-run")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 || hist[0].Activity != "print" {
		t.Fatalf("unexpected history: %+v", hist)
	}
}

func TestStore_GetStepProgress(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	// Empty executions return an empty slice, not an error.
	got, err := store.GetStepProgress(ctx, "nobody")
	if err != nil {
		t.Fatalf("empty get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %+v", got)
	}

	t0 := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	rows := []workflow.StepProgress{
		{
			StepName:     "second",
			BranchID:     "main",
			Status:       workflow.StepStatusCompleted,
			ActivityName: "print",
			Attempt:      1,
			StartedAt:    t0.Add(5 * time.Second),
			FinishedAt:   t0.Add(6 * time.Second),
		},
		{
			StepName:     "first",
			BranchID:     "main",
			Status:       workflow.StepStatusCompleted,
			ActivityName: "print",
			Attempt:      1,
			StartedAt:    t0,
			FinishedAt:   t0.Add(1 * time.Second),
			Detail: &workflow.ProgressDetail{
				Message: "halfway",
				Data:    map[string]any{"pct": float64(50)},
			},
		},
		{
			StepName:     "pending-step",
			BranchID:     "main",
			Status:       workflow.StepStatusPending,
			ActivityName: "print",
			Attempt:      0,
		},
	}
	for _, p := range rows {
		if err := store.UpdateStepProgress(ctx, "run-sp", p); err != nil {
			t.Fatalf("update: %v", err)
		}
	}

	// Progress for a different execution must not leak.
	if err := store.UpdateStepProgress(ctx, "other-run", workflow.StepProgress{
		StepName: "x", BranchID: "main", Status: workflow.StepStatusRunning, ActivityName: "print", Attempt: 1,
		StartedAt: t0,
	}); err != nil {
		t.Fatalf("update other: %v", err)
	}

	got, err = store.GetStepProgress(ctx, "run-sp")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(got), got)
	}
	// Ordered by started_at NULLS LAST, step_name, branch_id:
	// "first" (t0) → "second" (t0+5s) → "pending-step" (NULL).
	if got[0].StepName != "first" || got[1].StepName != "second" || got[2].StepName != "pending-step" {
		t.Fatalf("unexpected order: [%s, %s, %s]", got[0].StepName, got[1].StepName, got[2].StepName)
	}
	// Detail round-trips.
	if got[0].Detail == nil || got[0].Detail.Message != "halfway" || got[0].Detail.Data["pct"] != float64(50) {
		t.Fatalf("detail round-trip mismatch: %+v", got[0].Detail)
	}
	// Pending row has zero started_at.
	if !got[2].StartedAt.IsZero() || !got[2].FinishedAt.IsZero() {
		t.Fatalf("expected zero times on pending row: %+v", got[2])
	}
	// Status parses back correctly.
	if got[0].Status != workflow.StepStatusCompleted || got[2].Status != workflow.StepStatusPending {
		t.Fatalf("status mismatch: %+v", got)
	}
}

func TestStore_EnqueueTxRollbackAndCommit(t *testing.T) {
	store, pool := openTestStore(t)
	ctx := context.Background()

	// Rollback path: an EnqueueTx inside a tx that the caller
	// aborts must leave no row behind.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.EnqueueTx(ctx, tx, worker.NewRun{ID: "rb-run", Spec: []byte(`{}`)}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("enqueue tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := store.GetRun(ctx, "", "rb-run"); !errors.Is(err, runquery.ErrRunNotFound) {
		t.Fatalf("expected not found after rollback, got err=%v", err)
	}

	// Commit path: EnqueueTx + another write in the same tx are
	// both visible after commit.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if err := store.EnqueueTx(ctx, tx2, worker.NewRun{
		ID: "cm-run", Spec: []byte(`{}`), OrgID: "org-x",
	}); err != nil {
		_ = tx2.Rollback(ctx)
		t.Fatalf("enqueue tx 2: %v", err)
	}
	// Write to an adjacent row inside the same tx to simulate a
	// credit ledger debit: both rows must commit atomically.
	if _, err := tx2.Exec(ctx, `
		INSERT INTO workflow_credit_ledger (id, org_id, run_id, amount, reason, created_at)
		VALUES ('led-1', 'org-x', 'cm-run', -5, 'debit', NOW())
	`); err != nil {
		_ = tx2.Rollback(ctx)
		t.Fatalf("ledger insert: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	run, err := store.GetRun(ctx, "org-x", "cm-run")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.ID != "cm-run" || run.OrgID != "org-x" {
		t.Fatalf("unexpected run: %+v", run)
	}
	var ledgerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_credit_ledger WHERE run_id = $1`, "cm-run").Scan(&ledgerCount); err != nil {
		t.Fatalf("ledger count: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("expected 1 ledger row, got %d", ledgerCount)
	}

	// Nil tx is rejected.
	if err := store.EnqueueTx(ctx, nil, worker.NewRun{ID: "nil-tx", Spec: []byte(`{}`)}); err == nil {
		t.Fatalf("expected error for nil tx")
	}
}

func TestStore_UpdateRunSpecFencing(t *testing.T) {
	store, pool := openTestStore(t)
	ctx := context.Background()

	if err := store.Enqueue(ctx, worker.NewRun{ID: "spec-run", Spec: []byte(`{"v":1}`)}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, err := store.ClaimQueued(ctx, "w1")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v / %+v", err, claim)
	}

	// Happy path: same lease updates the spec.
	newSpec := []byte(`{"v":2}`)
	if err := store.UpdateRunSpec(ctx, claim, newSpec); err != nil {
		t.Fatalf("update spec: %v", err)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT spec FROM workflow_runs WHERE id = $1`, "spec-run").Scan(&stored); err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if string(stored) != `{"v":2}` {
		t.Fatalf("spec not updated: %s", stored)
	}

	// Wrong worker rejected.
	wrong := *claim
	wrong.WorkerID = "w2"
	if err := store.UpdateRunSpec(ctx, &wrong, []byte(`{"v":3}`)); err != worker.ErrLeaseLost {
		t.Fatalf("wrong worker: expected ErrLeaseLost, got %v", err)
	}

	// Wrong attempt rejected.
	badAttempt := *claim
	badAttempt.Attempt = 99
	if err := store.UpdateRunSpec(ctx, &badAttempt, []byte(`{"v":3}`)); err != worker.ErrLeaseLost {
		t.Fatalf("wrong attempt: expected ErrLeaseLost, got %v", err)
	}

	// After completion, the run is no longer running, so updates
	// must fail even with the right lease.
	if err := store.Complete(ctx, claim, worker.Outcome{Status: worker.StatusCompleted}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.UpdateRunSpec(ctx, claim, []byte(`{"v":4}`)); err != worker.ErrLeaseLost {
		t.Fatalf("after complete: expected ErrLeaseLost, got %v", err)
	}

	// Nil claim rejected.
	if err := store.UpdateRunSpec(ctx, nil, []byte(`{"v":5}`)); err == nil {
		t.Fatalf("expected error for nil claim")
	}
}
