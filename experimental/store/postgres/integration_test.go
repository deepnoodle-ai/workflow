package postgres_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deepnoodle-ai/workflow/experimental/store/postgres"
	"github.com/deepnoodle-ai/workflow/experimental/worker"
	"github.com/deepnoodle-ai/workflow/experimental/worker/runquery"
)

func TestStore_CustomSchema(t *testing.T) {
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
	t.Cleanup(func() { pool.Close() })

	// Migrate creates the schema; drop any pre-existing one first.
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS wf_alt CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}

	store := postgres.New(pool, postgres.WithSchema("wf_alt"))
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS wf_alt CASCADE`)
	})

	// Verify the table landed in the custom schema, not public.
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'wf_alt' AND table_name = 'workflow_runs'
	`).Scan(&n); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if n != 1 {
		t.Fatalf("workflow_runs not found in wf_alt schema")
	}

	// Exercise enqueue/claim/complete against the alt schema.
	if err := store.Enqueue(ctx, worker.NewRun{ID: "alt-run", Spec: []byte(`{}`)}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, err := store.ClaimQueued(ctx, "w")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v / %+v", err, claim)
	}
	if err := store.Complete(ctx, claim, worker.Outcome{Status: worker.StatusCompleted}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestStore_MetadataAndIdentityRoundTrip(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	run := worker.NewRun{
		ID:           "meta-run",
		Spec:         []byte(`{}`),
		OrgID:        "org-1",
		ProjectID:    "proj-1",
		ParentRunID:  "parent-1",
		WorkflowType: "demo",
		InitiatedBy:  "user-1",
		CreditCost:   5,
		CallbackURL:  "https://example.test/cb",
		Metadata:     map[string]string{"tag": "blue", "env": "test"},
	}
	if err := store.Enqueue(ctx, run); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claim, err := store.ClaimQueued(ctx, "w")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v / %+v", err, claim)
	}
	if claim.OrgID != "org-1" ||
		claim.ProjectID != "proj-1" ||
		claim.ParentRunID != "parent-1" ||
		claim.WorkflowType != "demo" ||
		claim.InitiatedBy != "user-1" ||
		claim.CreditCost != 5 ||
		claim.CallbackURL != "https://example.test/cb" {
		t.Fatalf("claim fields mismatch: %+v", claim)
	}
	if claim.Metadata["tag"] != "blue" || claim.Metadata["env"] != "test" {
		t.Fatalf("metadata mismatch: %+v", claim.Metadata)
	}

	got, err := store.GetRun(ctx, "org-1", "meta-run")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Metadata["tag"] != "blue" || got.Metadata["env"] != "test" {
		t.Fatalf("GetRun metadata mismatch: %+v", got.Metadata)
	}
	if got.ProjectID != "proj-1" || got.ParentRunID != "parent-1" || got.InitiatedBy != "user-1" {
		t.Fatalf("GetRun identity mismatch: %+v", got)
	}
}

func TestStore_DeadLetterStaleReturnsRunMetadata(t *testing.T) {
	store, pool := openTestStore(t)
	ctx := context.Background()

	_ = store.Enqueue(ctx, worker.NewRun{
		ID:           "dlq-run",
		Spec:         []byte(`{}`),
		OrgID:        "org-x",
		WorkflowType: "billed",
		CreditCost:   7,
	})
	claim, _ := store.ClaimQueued(ctx, "w")
	_ = claim

	past := time.Now().Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `
		UPDATE workflow_runs
		SET heartbeat_at = $1, attempt = 3
		WHERE id = $2
	`, past, "dlq-run"); err != nil {
		t.Fatalf("force stale: %v", err)
	}

	dead, err := store.DeadLetterStale(ctx, time.Now().Add(-time.Minute), 3, nil)
	if err != nil {
		t.Fatalf("dead-letter: %v", err)
	}
	if len(dead) != 1 {
		t.Fatalf("expected 1 dead-lettered run, got %d", len(dead))
	}
	if dead[0].ID != "dlq-run" ||
		dead[0].OrgID != "org-x" ||
		dead[0].WorkflowType != "billed" ||
		dead[0].CreditCost != 7 {
		t.Fatalf("dead-lettered run mismatch: %+v", dead[0])
	}
}

func TestStore_ListRefundPending(t *testing.T) {
	store, pool := openTestStore(t)
	ctx := context.Background()

	// refund-pending: failed run with a debit and no refund.
	_ = store.Enqueue(ctx, worker.NewRun{
		ID:           "needs-refund",
		Spec:         []byte(`{}`),
		OrgID:        "org-1",
		WorkflowType: "billed",
		CreditCost:   10,
	})
	// already-refunded: failed run with a debit and a refund.
	_ = store.Enqueue(ctx, worker.NewRun{
		ID:           "already-refunded",
		Spec:         []byte(`{}`),
		OrgID:        "org-1",
		WorkflowType: "billed",
		CreditCost:   10,
	})
	// no-cost: failed run with credit_cost=0 (excluded).
	_ = store.Enqueue(ctx, worker.NewRun{
		ID:           "no-cost",
		Spec:         []byte(`{}`),
		OrgID:        "org-1",
		WorkflowType: "free",
	})

	for _, id := range []string{"needs-refund", "already-refunded", "no-cost"} {
		claim, _ := store.ClaimQueued(ctx, "w")
		_ = claim
		_ = id
	}

	// Force all three to failed.
	if _, err := pool.Exec(ctx, `
		UPDATE workflow_runs
		SET status = 'failed', completed_at = NOW()
		WHERE id IN ('needs-refund','already-refunded','no-cost')
	`); err != nil {
		t.Fatalf("force failed: %v", err)
	}
	// Debit the two billable runs.
	if err := store.Debit(ctx, "org-1", "needs-refund", "billed", 10); err != nil {
		t.Fatalf("debit: %v", err)
	}
	if err := store.Debit(ctx, "org-1", "already-refunded", "billed", 10); err != nil {
		t.Fatalf("debit: %v", err)
	}
	// Refund the second one.
	if err := store.Refund(ctx, "org-1", "already-refunded", "billed", 10); err != nil {
		t.Fatalf("refund: %v", err)
	}

	failed, err := store.ListRefundPending(ctx, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(failed) != 1 || failed[0].ID != "needs-refund" {
		t.Fatalf("expected [needs-refund], got %+v", failed)
	}
	if failed[0].CreditCost != 10 || failed[0].OrgID != "org-1" || failed[0].WorkflowType != "billed" {
		t.Fatalf("failed run fields mismatch: %+v", failed[0])
	}
}

func TestStore_RunReadAPI(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	// Seed three runs in org-1 with different workflow types.
	for _, r := range []worker.NewRun{
		{ID: "r1", Spec: []byte(`{}`), OrgID: "org-1", WorkflowType: "a", Metadata: map[string]string{"k": "v1"}},
		{ID: "r2", Spec: []byte(`{}`), OrgID: "org-1", WorkflowType: "b", Metadata: map[string]string{"k": "v2"}},
		{ID: "r3", Spec: []byte(`{}`), OrgID: "org-1", WorkflowType: "a", Metadata: map[string]string{"k": "v1"}},
		// one in a different org to prove scoping
		{ID: "r4", Spec: []byte(`{}`), OrgID: "org-2", WorkflowType: "a"},
	} {
		if err := store.Enqueue(ctx, r); err != nil {
			t.Fatalf("enqueue %s: %v", r.ID, err)
		}
	}

	// GetRun: wrong org returns ErrRunNotFound.
	if _, err := store.GetRun(ctx, "org-2", "r1"); !errors.Is(err, postgres.ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}

	// CountRuns: filter by workflow_type.
	n, err := store.CountRuns(ctx, "org-1", postgres.RunFilter{WorkflowType: "a"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("count: got %d, want 2", n)
	}

	// ListRuns: metadata containment.
	list, _, err := store.ListRuns(ctx, "org-1", postgres.RunFilter{
		Metadata: map[string]string{"k": "v1"},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list: got %d, want 2", len(list))
	}

	// ListRuns: pagination.
	page1, cursor, err := store.ListRuns(ctx, "org-1", postgres.RunFilter{Limit: 2})
	if err != nil {
		t.Fatalf("list page 1: %v", err)
	}
	if len(page1) != 2 || cursor == nil {
		t.Fatalf("list page 1: got %d rows / cursor %+v", len(page1), cursor)
	}
	page2, cursor2, err := store.ListRuns(ctx, "org-1", postgres.RunFilter{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(page2) != 1 || cursor2 != nil {
		t.Fatalf("list page 2: got %d rows / cursor %+v", len(page2), cursor2)
	}

	// DeleteRun: refuses running runs.
	claim, err := store.ClaimQueued(ctx, "w")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v / %+v", err, claim)
	}
	if err := store.DeleteRun(ctx, "org-1", claim.ID); !errors.Is(err, postgres.ErrCannotDeleteRunning) {
		t.Fatalf("expected ErrCannotDeleteRunning, got %v", err)
	}
	// DeleteRun: unknown id.
	if err := store.DeleteRun(ctx, "org-1", "nope"); !errors.Is(err, postgres.ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}
}

// TestStore_UpgradeFromV003 seeds a v0.0.3-shaped workflow_runs table
// (org_id NOT NULL DEFAULT '', initiated_by NOT NULL DEFAULT '', no
// project_id/parent_run_id/metadata columns) with a populated row,
// then runs Migrate and confirms the new read API can still find the
// row. This catches the regression the review flagged: without the
// UPDATE ... SET org_id = NULL WHERE org_id = '' step, pre-existing
// single-tenant rows become invisible to GetRun / ListRuns.
func TestStore_UpgradeFromV003(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("set %s to run postgres store tests", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	// Fresh schema so the v0.0.3 DDL below is what Migrate sees.
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS wf_upgrade CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA wf_upgrade`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS wf_upgrade CASCADE`)
	})

	// v0.0.3 workflow_runs shape: org_id / initiated_by NOT NULL '',
	// no project_id / parent_run_id / metadata columns.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE wf_upgrade.workflow_runs (
		    id              TEXT PRIMARY KEY,
		    spec            BYTEA NOT NULL,
		    status          TEXT NOT NULL,
		    attempt         INTEGER NOT NULL DEFAULT 0,
		    claimed_by      TEXT NOT NULL DEFAULT '',
		    heartbeat_at    TIMESTAMPTZ,
		    checkpoint      BYTEA,
		    result          BYTEA,
		    error_message   TEXT NOT NULL DEFAULT '',
		    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    started_at      TIMESTAMPTZ,
		    completed_at    TIMESTAMPTZ,
		    org_id          TEXT NOT NULL DEFAULT '',
		    workflow_type   TEXT NOT NULL DEFAULT '',
		    initiated_by    TEXT NOT NULL DEFAULT '',
		    credit_cost     INTEGER NOT NULL DEFAULT 0,
		    callback_url    TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("seed v0.0.3 table: %v", err)
	}
	// Insert a populated single-tenant row with the old sentinels.
	if _, err := pool.Exec(ctx, `
		INSERT INTO wf_upgrade.workflow_runs (id, spec, status, workflow_type)
		VALUES ('legacy-run', $1, 'completed', 'demo')
	`, []byte(`{}`)); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// Run the upgrade.
	store := postgres.New(pool, postgres.WithSchema("wf_upgrade"))
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The row should still be findable via the single-tenant API
	// (orgID == "" matches org_id IS NULL). If the UPDATE step is
	// missing from schema.sql, this GetRun returns ErrRunNotFound.
	got, err := store.GetRun(ctx, "", "legacy-run")
	if err != nil {
		t.Fatalf("GetRun after upgrade: %v", err)
	}
	if got.ID != "legacy-run" || got.WorkflowType != "demo" {
		t.Fatalf("upgraded row mismatch: %+v", got)
	}

	// And ListRuns in single-tenant mode should see it too.
	list, _, err := store.ListRuns(ctx, "", runquery.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns after upgrade: %v", err)
	}
	if len(list) != 1 || list[0].ID != "legacy-run" {
		t.Fatalf("ListRuns after upgrade: got %+v", list)
	}

	// Re-running Migrate must be a no-op (all statements idempotent).
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

// TestStore_ClaimQueuedConcurrent spins up several goroutines claiming
// a fixed-size pool of queued runs. Every run must be claimed exactly
// once across all workers — no duplicates, no drops.
func TestStore_ClaimQueuedConcurrent(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	const numRuns = 20
	for i := 0; i < numRuns; i++ {
		id := "concurrent-" + strconv.Itoa(i)
		if err := store.Enqueue(ctx, worker.NewRun{ID: id, Spec: []byte(`{}`)}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	const numWorkers = 8
	var (
		mu     sync.Mutex
		claims = map[string]string{} // runID -> workerID
		wg     sync.WaitGroup
	)
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		workerID := "w-" + strconv.Itoa(w)
		go func(workerID string) {
			defer wg.Done()
			for {
				claim, err := store.ClaimQueued(ctx, workerID)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if claim == nil {
					return
				}
				mu.Lock()
				if prev, dup := claims[claim.ID]; dup {
					t.Errorf("duplicate claim for %s: %s and %s", claim.ID, prev, workerID)
				}
				claims[claim.ID] = workerID
				mu.Unlock()
			}
		}(workerID)
	}
	wg.Wait()

	if len(claims) != numRuns {
		t.Fatalf("claimed %d runs, want %d", len(claims), numRuns)
	}
}

