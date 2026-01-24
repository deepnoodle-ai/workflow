package workflow_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/memory"
)

// testCallbacks tracks callback invocations
type testCallbacks struct {
	workflow.BaseEngineCallbacks
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

func createTestWorkflow(t *testing.T) *workflow.Workflow {
	wf, err := workflow.New(workflow.Options{
		Name: "test-workflow",
		Steps: []*workflow.Step{
			{Name: "start", Activity: "test-activity"},
		},
	})
	assert.NoError(t, err)
	return wf
}

func createTestEngine(t *testing.T, runners map[string]workflow.Runner) (*workflow.Engine, *testCallbacks) {
	store := memory.NewStore()
	callbacks := &testCallbacks{}

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:             store,
		Callbacks:         callbacks,
		Runners:           runners,
		WorkerID:          "test-worker",
		MaxConcurrent:     10,
		HeartbeatInterval: 100 * time.Millisecond,
		PollInterval:      50 * time.Millisecond,
	})
	assert.NoError(t, err)

	return engine, callbacks
}

func TestNewEngine_Validation(t *testing.T) {
	t.Run("missing store fails", func(t *testing.T) {
		_, err := workflow.NewEngine(workflow.EngineOptions{
			WorkerID: "w1",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "store is required")
	})

	t.Run("missing worker ID fails", func(t *testing.T) {
		_, err := workflow.NewEngine(workflow.EngineOptions{
			Store: memory.NewStore(),
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "worker ID is required")
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		engine, err := workflow.NewEngine(workflow.EngineOptions{
			Store:    memory.NewStore(),
			WorkerID: "w1",
		})
		assert.NoError(t, err)
		assert.NotNil(t, engine)
	})
}

func TestEngine_Start(t *testing.T) {
	engine, _ := createTestEngine(t, nil)

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
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return map[string]any{"result": true}, nil
			},
		},
	}
	engine, callbacks := createTestEngine(t, runners)
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit without starting should still persist
	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, handle.ID)
	assert.Equal(t, handle.Status, workflow.EngineStatusPending)
	assert.Equal(t, callbacks.submitted.Load(), int32(1))

	// Verify record was persisted
	record, err := engine.Get(ctx, handle.ID)
	assert.NoError(t, err)
	assert.Equal(t, record.ID, handle.ID)
}

func TestEngine_SubmitWithCustomID(t *testing.T) {
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return map[string]any{"result": true}, nil
			},
		},
	}
	engine, _ := createTestEngine(t, runners)
	wf := createTestWorkflow(t)

	ctx := context.Background()

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow:    wf,
		ExecutionID: "custom-id-123",
		Inputs:      map[string]any{},
	})
	assert.NoError(t, err)
	assert.Equal(t, handle.ID, "custom-id-123")
}

func TestEngine_Get(t *testing.T) {
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return map[string]any{"result": true}, nil
			},
		},
	}
	engine, _ := createTestEngine(t, runners)
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit
	handle, _ := engine.Submit(ctx, workflow.SubmitRequest{
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
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return map[string]any{"result": true}, nil
			},
		},
	}
	engine, _ := createTestEngine(t, runners)
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit multiple
	for i := 0; i < 3; i++ {
		engine.Submit(ctx, workflow.SubmitRequest{
			Workflow: wf,
			Inputs:   map[string]any{},
		})
	}

	// List all
	records, err := engine.List(ctx, workflow.ExecutionFilter{})
	assert.NoError(t, err)
	assert.Len(t, records, 3)
}

func TestEngine_SubmitAndComplete(t *testing.T) {
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return map[string]any{"result": true}, nil
			},
		},
	}
	engine, callbacks := createTestEngine(t, runners)
	wf, err := workflow.New(workflow.Options{
		Name: "test-workflow",
		Steps: []*workflow.Step{
			{Name: "start", Activity: "test-activity", Store: "success"},
		},
		Outputs: []*workflow.Output{
			{Name: "result", Variable: "success", Path: "main"},
		},
	})
	assert.NoError(t, err)

	ctx := context.Background()

	// Start engine
	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Submit
	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	var record *workflow.ExecutionRecord
	for i := 0; i < 50; i++ {
		record, _ = engine.Get(ctx, handle.ID)
		if record.Status == workflow.EngineStatusCompleted || record.Status == workflow.EngineStatusFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	assert.Equal(t, record.Status, workflow.EngineStatusCompleted, "LastError: %s", record.LastError)
	assert.Equal(t, callbacks.submitted.Load(), int32(1))
	assert.Equal(t, callbacks.completed.Load(), int32(1))

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}

func TestEngine_ConcurrentExecutions(t *testing.T) {
	callCount := atomic.Int32{}
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				callCount.Add(1)
				time.Sleep(50 * time.Millisecond) // Simulate work
				return map[string]any{"done": true}, nil
			},
		},
	}

	store := memory.NewStore()
	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:             store,
		Runners:           runners,
		WorkerID:          "test-worker",
		MaxConcurrent:     5, // Allow 5 concurrent executions
		HeartbeatInterval: 100 * time.Millisecond,
		PollInterval:      50 * time.Millisecond,
	})
	assert.NoError(t, err)

	wf, _ := workflow.New(workflow.Options{
		Name:  "test-workflow",
		Steps: []*workflow.Step{{Name: "start", Activity: "test-activity"}},
	})

	ctx := context.Background()
	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Submit 10 executions
	for i := 0; i < 10; i++ {
		_, err := engine.Submit(ctx, workflow.SubmitRequest{
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
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				time.Sleep(200 * time.Millisecond) // Simulate work
				return map[string]any{"done": true}, nil
			},
		},
	}
	engine, _ := createTestEngine(t, runners)
	wf := createTestWorkflow(t)

	ctx := context.Background()
	engine.Start(ctx)

	// Submit an execution
	engine.Submit(ctx, workflow.SubmitRequest{
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
	started := make(chan struct{})
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				close(started) // Signal that we started
				time.Sleep(5 * time.Second) // Long-running work
				return map[string]any{"done": true}, nil
			},
		},
	}
	engine, _ := createTestEngine(t, runners)
	wf := createTestWorkflow(t)

	ctx := context.Background()
	engine.Start(ctx)

	// Submit an execution
	engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})

	// Wait for the task to actually start executing
	select {
	case <-started:
		// Good, task started
	case <-time.After(time.Second):
		t.Fatal("task never started")
	}

	// Shutdown should timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := engine.Shutdown(shutdownCtx)
	assert.Error(t, err)
	assert.Equal(t, err, context.DeadlineExceeded)
}

func TestEngine_Cancel(t *testing.T) {
	runners := map[string]workflow.Runner{
		"test-activity": &workflow.InlineRunner{
			Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
				return map[string]any{"result": true}, nil
			},
		},
	}
	engine, _ := createTestEngine(t, runners)
	wf := createTestWorkflow(t)

	ctx := context.Background()

	// Submit (don't start engine)
	handle, _ := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})

	// Cancel pending execution
	err := engine.Cancel(ctx, handle.ID)
	assert.NoError(t, err)

	// Check status
	record, _ := engine.Get(ctx, handle.ID)
	assert.Equal(t, record.Status, workflow.EngineStatusCancelled)
}

func TestEngine_StaleTaskRecovery(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()

	// Pre-populate store with stale running task
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusRunning,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().Add(-time.Hour),
		StartedAt:    time.Now().Add(-time.Hour),
	}
	err := store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	staleTask := &workflow.TaskRecord{
		ID:            "task-1",
		ExecutionID:   "exec-1",
		StepName:      "step1",
		Attempt:       1,
		Status:        workflow.TaskStatusRunning,
		Spec:          &workflow.TaskSpec{Type: "inline"},
		WorkerID:      "dead-worker",
		LastHeartbeat: time.Now().Add(-time.Hour), // Very old
		VisibleAt:     time.Now().Add(-time.Hour),
		CreatedAt:     time.Now().Add(-time.Hour),
		StartedAt:     time.Now().Add(-time.Hour),
	}
	err = store.CreateTask(ctx, staleTask)
	assert.NoError(t, err)

	// Create engine - recovery should reset the stale task
	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:            store,
		WorkerID:         "test-worker",
		HeartbeatTimeout: 100 * time.Millisecond, // Short timeout
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Verify task was reset
	task, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, task.Status, workflow.TaskStatusPending)
	assert.Equal(t, task.Attempt, 2) // Attempt incremented

	shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}

func TestEngine_Reaper_StaleRunning(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()

	// Create engine with short reaper interval
	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:            store,
		WorkerID:         "test-worker",
		ReaperInterval:   50 * time.Millisecond,
		HeartbeatTimeout: 100 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Create execution and stale task after engine started
	exec := &workflow.ExecutionRecord{
		ID:           "stale-exec",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusRunning,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now(),
		StartedAt:    time.Now(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	staleTask := &workflow.TaskRecord{
		ID:            "stale-task",
		ExecutionID:   "stale-exec",
		StepName:      "step1",
		Attempt:       1,
		Status:        workflow.TaskStatusRunning,
		Spec:          &workflow.TaskSpec{Type: "inline"},
		WorkerID:      "dead-worker",
		LastHeartbeat: time.Now().Add(-time.Hour), // Very old heartbeat
		VisibleAt:     time.Now(),
		CreatedAt:     time.Now(),
		StartedAt:     time.Now(),
	}
	err = store.CreateTask(ctx, staleTask)
	assert.NoError(t, err)

	// Wait for reaper to detect and reset
	time.Sleep(300 * time.Millisecond)

	// Verify task was reset
	task, err := store.GetTask(ctx, "stale-task")
	assert.NoError(t, err)
	assert.Equal(t, task.Status, workflow.TaskStatusPending)
	assert.Equal(t, task.Attempt, 2)

	shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	engine.Shutdown(shutdownCtx)
}
