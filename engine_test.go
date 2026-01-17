package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"
)

// testActivity is a simple activity for testing
type testActivity struct {
	name   string
	result any
	err    error
	called atomic.Int32
}

func newTestActivity(name string, result any, err error) *testActivity {
	return &testActivity{name: name, result: result, err: err}
}

func (a *testActivity) Name() string {
	return a.name
}

func (a *testActivity) Execute(ctx Context, params map[string]any) (any, error) {
	a.called.Add(1)
	return a.result, a.err
}

// testCallbacks tracks callback invocations
type testCallbacks struct {
	BaseEngineCallbacks
	submitted atomic.Int32
	started   atomic.Int32
	completed atomic.Int32
}

func (c *testCallbacks) OnExecutionSubmitted(id string, workflowName string) {
	c.submitted.Add(1)
}

func (c *testCallbacks) OnExecutionStarted(id string) {
	c.started.Add(1)
}

func (c *testCallbacks) OnExecutionCompleted(id string, duration time.Duration, err error) {
	c.completed.Add(1)
}

func createTestWorkflow(t *testing.T) *Workflow {
	wf, err := New(Options{
		Name: "test-workflow",
		Steps: []*Step{
			{Name: "start", Activity: "test-activity"},
		},
	})
	assert.NoError(t, err)
	return wf
}

func createTestEngine(t *testing.T, activities []Activity) (*Engine, *testCallbacks) {
	store := NewMemoryStore()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "test-worker",
		BufferSize: 100,
		LeaseTTL:   5 * time.Minute,
	})
	env := NewLocalEnvironment()
	callbacks := &testCallbacks{}

	engine, err := NewEngine(EngineOptions{
		Store:             store,
		Queue:             queue,
		Environment:       env,
		Callbacks:         callbacks,
		Activities:        activities,
		WorkerID:          "test-worker",
		MaxConcurrent:     10,
		HeartbeatInterval: 100 * time.Millisecond,
	})
	assert.NoError(t, err)

	return engine, callbacks
}

func TestNewEngine_Validation(t *testing.T) {
	t.Run("missing store fails", func(t *testing.T) {
		_, err := NewEngine(EngineOptions{
			Queue:       NewMemoryQueue(MemoryQueueOptions{WorkerID: "w1"}),
			Environment: NewLocalEnvironment(),
			WorkerID:    "w1",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "store is required")
	})

	t.Run("missing queue fails", func(t *testing.T) {
		_, err := NewEngine(EngineOptions{
			Store:       NewMemoryStore(),
			Environment: NewLocalEnvironment(),
			WorkerID:    "w1",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "queue is required")
	})

	t.Run("missing environment fails", func(t *testing.T) {
		_, err := NewEngine(EngineOptions{
			Store:    NewMemoryStore(),
			Queue:    NewMemoryQueue(MemoryQueueOptions{WorkerID: "w1"}),
			WorkerID: "w1",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "environment is required")
	})

	t.Run("missing worker ID fails", func(t *testing.T) {
		_, err := NewEngine(EngineOptions{
			Store:       NewMemoryStore(),
			Queue:       NewMemoryQueue(MemoryQueueOptions{WorkerID: "w1"}),
			Environment: NewLocalEnvironment(),
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "worker ID is required")
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		engine, err := NewEngine(EngineOptions{
			Store:       NewMemoryStore(),
			Queue:       NewMemoryQueue(MemoryQueueOptions{WorkerID: "w1"}),
			Environment: NewLocalEnvironment(),
			WorkerID:    "w1",
		})
		assert.NoError(t, err)
		assert.NotNil(t, engine)
	})
}

func TestEngine_Start(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	engine, _ := createTestEngine(t, []Activity{activity})

	ctx := context.Background()
	err := engine.Start(ctx)
	assert.NoError(t, err)

	// Starting again should fail
	err = engine.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}

func TestEngine_Submit(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	engine, callbacks := createTestEngine(t, []Activity{activity})
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit without starting should still persist
	handle, err := engine.Submit(ctx, SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, handle.ID)
	assert.Equal(t, handle.Status, EngineStatusPending)
	assert.Equal(t, callbacks.submitted.Load(), int32(1))

	// Verify record was persisted
	record, err := engine.Get(ctx, handle.ID)
	assert.NoError(t, err)
	assert.Equal(t, record.ID, handle.ID)
	assert.Equal(t, record.Status, EngineStatusPending)
}

func TestEngine_SubmitWithCustomID(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	engine, _ := createTestEngine(t, []Activity{activity})
	wf := createTestWorkflow(t)

	ctx := context.Background()

	handle, err := engine.Submit(ctx, SubmitRequest{
		Workflow:    wf,
		ExecutionID: "custom-id-123",
		Inputs:      map[string]any{},
	})
	assert.NoError(t, err)
	assert.Equal(t, handle.ID, "custom-id-123")
}

func TestEngine_Get(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	engine, _ := createTestEngine(t, []Activity{activity})
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit
	handle, _ := engine.Submit(ctx, SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})

	// Get existing
	record, err := engine.Get(ctx, handle.ID)
	assert.NoError(t, err)
	assert.Equal(t, record.ID, handle.ID)

	// Get non-existent
	_, err = engine.Get(ctx, "non-existent")
	assert.Error(t, err)
}

func TestEngine_List(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	engine, _ := createTestEngine(t, []Activity{activity})
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit multiple
	for i := 0; i < 3; i++ {
		engine.Submit(ctx, SubmitRequest{
			Workflow: wf,
			Inputs:   map[string]any{},
		})
	}

	// List all
	records, err := engine.List(ctx, ListFilter{})
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// Filter by status
	records, err = engine.List(ctx, ListFilter{Statuses: []EngineExecutionStatus{EngineStatusPending}})
	assert.NoError(t, err)
	assert.Len(t, records, 3)
}

func TestEngine_SubmitAndComplete(t *testing.T) {
	activity := newTestActivity("test-activity", true, nil)
	engine, callbacks := createTestEngine(t, []Activity{activity})
	wf, err := New(Options{
		Name: "test-workflow",
		Steps: []*Step{
			{Name: "start", Activity: "test-activity", Store: "success"},
		},
		Outputs: []*Output{
			{Name: "result", Variable: "success", Path: "main"},
		},
	})
	assert.NoError(t, err)

	ctx := context.Background()

	// Start engine
	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Submit
	handle, err := engine.Submit(ctx, SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	var record *ExecutionRecord
	for i := 0; i < 50; i++ {
		record, _ = engine.Get(ctx, handle.ID)
		if record.Status == EngineStatusCompleted || record.Status == EngineStatusFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	assert.Equal(t, record.Status, EngineStatusCompleted, "LastError: %s", record.LastError)
	assert.Equal(t, record.Outputs["result"], true)
	assert.Equal(t, callbacks.submitted.Load(), int32(1))
	assert.Equal(t, callbacks.started.Load(), int32(1))
	assert.Equal(t, callbacks.completed.Load(), int32(1))
	assert.Equal(t, activity.called.Load(), int32(1))

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}

func TestEngine_ConcurrentExecutions(t *testing.T) {
	callCount := atomic.Int32{}
	activity := NewActivityFunction("test-activity", func(ctx Context, params map[string]any) (any, error) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond) // Simulate work
		return "done", nil
	})

	store := NewMemoryStore()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "test-worker",
		BufferSize: 100,
		LeaseTTL:   5 * time.Minute,
	})
	env := NewLocalEnvironment()

	engine, err := NewEngine(EngineOptions{
		Store:             store,
		Queue:             queue,
		Environment:       env,
		Activities:        []Activity{activity},
		WorkerID:          "test-worker",
		MaxConcurrent:     5, // Allow 5 concurrent executions
		HeartbeatInterval: 100 * time.Millisecond,
	})
	assert.NoError(t, err)

	wf, _ := New(Options{
		Name:  "test-workflow",
		Steps: []*Step{{Name: "start", Activity: "test-activity"}},
	})

	ctx := context.Background()
	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Submit 10 executions
	for i := 0; i < 10; i++ {
		_, err := engine.Submit(ctx, SubmitRequest{
			Workflow: wf,
			Inputs:   map[string]any{},
		})
		assert.NoError(t, err)
	}

	// Wait for all to complete
	time.Sleep(500 * time.Millisecond)

	// Check all 10 were executed
	assert.Equal(t, callCount.Load(), int32(10))

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}

func TestEngine_Shutdown(t *testing.T) {
	activity := NewActivityFunction("test-activity", func(ctx Context, params map[string]any) (any, error) {
		time.Sleep(200 * time.Millisecond) // Simulate work
		return "done", nil
	})

	engine, _ := createTestEngine(t, []Activity{activity})
	wf := createTestWorkflow(t)

	ctx := context.Background()
	engine.Start(ctx)

	// Submit an execution
	engine.Submit(ctx, SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})

	// Give it time to start
	time.Sleep(50 * time.Millisecond)

	// Shutdown should wait for completion
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := engine.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

func TestEngine_ShutdownTimeout(t *testing.T) {
	activity := NewActivityFunction("test-activity", func(ctx Context, params map[string]any) (any, error) {
		time.Sleep(5 * time.Second) // Long-running work
		return "done", nil
	})

	engine, _ := createTestEngine(t, []Activity{activity})
	wf := createTestWorkflow(t)

	ctx := context.Background()
	engine.Start(ctx)

	// Submit an execution
	engine.Submit(ctx, SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})

	// Give it time to start
	time.Sleep(50 * time.Millisecond)

	// Shutdown should timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := engine.Shutdown(shutdownCtx)
	assert.Error(t, err)
	assert.Equal(t, err, context.DeadlineExceeded)
}

func TestEngine_Cancel(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	engine, _ := createTestEngine(t, []Activity{activity})
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit (don't start engine)
	handle, _ := engine.Submit(ctx, SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})

	// Cancel pending execution
	err := engine.Cancel(ctx, handle.ID)
	assert.NoError(t, err)

	// Check status
	record, _ := engine.Get(ctx, handle.ID)
	assert.Equal(t, record.Status, EngineStatusCancelled)
}

func TestEngine_RecoverOrphaned_Resume(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	store := NewMemoryStore()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "test-worker",
		BufferSize: 100,
		LeaseTTL:   5 * time.Minute,
	})
	env := NewLocalEnvironment()

	// Pre-populate store with orphaned executions (simulate crash)
	ctx := context.Background()
	store.Create(ctx, &ExecutionRecord{
		ID:           "orphan-pending",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().Add(-time.Hour),
	})
	store.Create(ctx, &ExecutionRecord{
		ID:            "orphan-running",
		WorkflowName:  "test-workflow",
		Status:        EngineStatusRunning,
		Inputs:        map[string]any{},
		Attempt:       1,
		WorkerID:      "dead-worker",
		LastHeartbeat: time.Now().Add(-time.Hour),
		CreatedAt:     time.Now().Add(-time.Hour),
		StartedAt:     time.Now().Add(-time.Hour),
	})

	wf := createTestWorkflow(t)

	// Create engine with resume mode
	engine, err := NewEngine(EngineOptions{
		Store:        store,
		Queue:        queue,
		Environment:  env,
		Workflows:    map[string]*Workflow{wf.Name(): wf},
		Activities:   []Activity{activity},
		WorkerID:     "test-worker",
		RecoveryMode: RecoveryResume,
	})
	assert.NoError(t, err)

	// Start engine - should recover orphaned executions
	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Both should now be completed (or at least re-enqueued)
	record1, _ := store.Get(ctx, "orphan-pending")
	record2, _ := store.Get(ctx, "orphan-running")

	// Check that attempts were incremented (fencing)
	assert.GreaterOrEqual(t, record1.Attempt, 2)
	assert.GreaterOrEqual(t, record2.Attempt, 2)

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}

func TestEngine_RecoverOrphaned_Fail(t *testing.T) {
	store := NewMemoryStore()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "test-worker",
		BufferSize: 100,
		LeaseTTL:   5 * time.Minute,
	})
	env := NewLocalEnvironment()

	// Pre-populate store with orphaned executions
	ctx := context.Background()
	store.Create(ctx, &ExecutionRecord{
		ID:           "orphan-pending",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().Add(-time.Hour),
	})
	store.Create(ctx, &ExecutionRecord{
		ID:            "orphan-running",
		WorkflowName:  "test-workflow",
		Status:        EngineStatusRunning,
		Inputs:        map[string]any{},
		Attempt:       1,
		WorkerID:      "dead-worker",
		LastHeartbeat: time.Now().Add(-time.Hour),
		CreatedAt:     time.Now().Add(-time.Hour),
		StartedAt:     time.Now().Add(-time.Hour),
	})

	// Create engine with fail mode
	engine, err := NewEngine(EngineOptions{
		Store:        store,
		Queue:        queue,
		Environment:  env,
		WorkerID:     "test-worker",
		RecoveryMode: RecoveryFail,
	})
	assert.NoError(t, err)

	// Start engine - should mark orphaned as failed
	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Check that both are marked as failed
	record1, _ := store.Get(ctx, "orphan-pending")
	record2, _ := store.Get(ctx, "orphan-running")

	assert.Equal(t, record1.Status, EngineStatusFailed)
	assert.Equal(t, record2.Status, EngineStatusFailed)
	assert.Contains(t, record1.LastError, "orphaned")
	assert.Contains(t, record2.LastError, "orphaned")

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}

func TestEngine_Reaper_StaleRunning(t *testing.T) {
	activity := newTestActivity("test-activity", "result", nil)
	store := NewMemoryStore()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "test-worker",
		BufferSize: 100,
		LeaseTTL:   5 * time.Minute,
	})
	env := NewLocalEnvironment()

	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Create engine with short reaper interval and heartbeat timeout
	engine, err := NewEngine(EngineOptions{
		Store:            store,
		Queue:            queue,
		Environment:      env,
		Workflows:        map[string]*Workflow{wf.Name(): wf},
		Activities:       []Activity{activity},
		WorkerID:         "test-worker",
		RecoveryMode:     RecoveryResume,
		ReaperInterval:   50 * time.Millisecond,
		HeartbeatTimeout: 100 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Inject a stale running execution directly into the store
	// (simulating a worker that crashed after claiming)
	staleRecord := &ExecutionRecord{
		ID:            "stale-running",
		WorkflowName:  "test-workflow",
		Status:        EngineStatusRunning,
		Inputs:        map[string]any{},
		Attempt:       1,
		WorkerID:      "dead-worker",
		LastHeartbeat: time.Now().Add(-time.Hour), // Very old heartbeat
		CreatedAt:     time.Now().Add(-time.Hour),
		StartedAt:     time.Now().Add(-time.Hour),
	}
	store.Create(ctx, staleRecord)

	// Wait for reaper to detect and recover
	time.Sleep(300 * time.Millisecond)

	// Check that the execution was recovered (attempt incremented, status reset)
	record, _ := store.Get(ctx, "stale-running")
	// Either still pending waiting to be processed, or completed
	// The key is that attempt was incremented
	assert.GreaterOrEqual(t, record.Attempt, 2, "attempt should be incremented by reaper")

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}
