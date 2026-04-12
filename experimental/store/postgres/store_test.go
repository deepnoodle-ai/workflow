package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/experimental/store/postgres"
	"github.com/deepnoodle-ai/workflow/experimental/worker"
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
	for _, tbl := range []string{"workflow_activity_log", "workflow_step_progress", "workflow_runs"} {
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

	again, err := store.ClaimQueued(ctx, "test-worker")
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("expected no more queued runs, got %+v", again)
	}

	lease := worker.Lease{RunID: claim.ID, WorkerID: "test-worker", Attempt: claim.Attempt}
	if err := store.Heartbeat(ctx, lease); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	if err := store.Complete(ctx, lease, worker.Outcome{
		Status: worker.StatusCompleted,
		Result: []byte(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestStore_HeartbeatLeaseLost(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	_ = store.Enqueue(ctx, worker.NewRun{ID: "run-b"})
	claim, _ := store.ClaimQueued(ctx, "alpha")

	// Wrong worker ID should be rejected.
	wrong := worker.Lease{RunID: claim.ID, WorkerID: "beta", Attempt: claim.Attempt}
	if err := store.Heartbeat(ctx, wrong); err != worker.ErrLeaseLost {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}
	// Wrong attempt should be rejected.
	badAttempt := worker.Lease{RunID: claim.ID, WorkerID: "alpha", Attempt: 99}
	if err := store.Heartbeat(ctx, badAttempt); err != worker.ErrLeaseLost {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}
}

func TestStore_ReclaimAndDeadLetter(t *testing.T) {
	store, pool := openTestStore(t)
	ctx := context.Background()

	_ = store.Enqueue(ctx, worker.NewRun{ID: "reclaim-me"})
	_ = store.Enqueue(ctx, worker.NewRun{ID: "dead-letter-me"})

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
	if len(dead) != 1 || dead[0] != "dead-letter-me" {
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

	_ = store.Enqueue(ctx, worker.NewRun{ID: "cp-run"})
	claim, _ := store.ClaimQueued(ctx, "w")

	cp := store.NewCheckpointer(worker.Lease{
		RunID:    claim.ID,
		WorkerID: "w",
		Attempt:  claim.Attempt,
	})

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
	bogus := store.NewCheckpointer(worker.Lease{
		RunID: claim.ID, WorkerID: "someone-else", Attempt: claim.Attempt,
	})
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

	_ = store.Enqueue(ctx, worker.NewRun{ID: "obs-run"})

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
