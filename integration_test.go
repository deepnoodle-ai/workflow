package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/engine"
	workflowhttp "github.com/deepnoodle-ai/workflow/internal/http"
	"github.com/deepnoodle-ai/workflow/internal/memory"
	"github.com/deepnoodle-ai/workflow/internal/services"
	"github.com/deepnoodle-ai/workflow/runners"
)

// TestHTTPTaskClaimAndComplete tests the basic task lifecycle via HTTP.
func TestHTTPTaskClaimAndComplete(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a task in the store
	task := &domain.TaskRecord{
		ID:           "task-1",
		ExecutionID:  "exec-1",
		StepName:     "step-1",
		ActivityName: "test-activity",
		Status:       domain.TaskStatusPending,
		Input: &domain.TaskInput{
			Type:  "inline",
			Input: map[string]any{"key": "value"},
		},
		CreatedAt: time.Now(),
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Create services
	taskService := services.NewTaskService(services.TaskServiceOptions{
		Tasks:  store,
		Events: store,
	})

	// Create HTTP server
	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		TaskService: taskService,
	})

	// Create test server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Create HTTP client for tasks
	client := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{
		BaseURL: ts.URL,
	})

	// Claim task
	claimed, err := client.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed)
	assert.Equal(t, claimed.ID, "task-1")
	assert.Equal(t, claimed.StepName, "step-1")

	// Send heartbeat
	err = client.HeartbeatTask(ctx, claimed.ID, "worker-1")
	assert.NoError(t, err)

	// Complete task
	result := &domain.TaskOutput{
		Success: true,
		Data:    map[string]any{"result": "completed"},
	}
	err = client.CompleteTask(ctx, claimed.ID, "worker-1", result)
	assert.NoError(t, err)

	// Verify task is completed
	updatedTask, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, updatedTask.Status, domain.TaskStatusCompleted)
}

// TestHTTPExecutionLifecycle tests execution operations via HTTP.
func TestHTTPExecutionLifecycle(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create an execution in the store
	exec := &domain.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       domain.ExecutionStatusRunning,
		Inputs:       map[string]any{"input": "value"},
		CreatedAt:    time.Now(),
		StartedAt:    time.Now(),
	}
	err := store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	// Create engine
	eng, err := engine.New(engine.Options{
		Store:    store,
		WorkerID: "test-worker",
		Mode:     engine.ModeServer,
	})
	assert.NoError(t, err)

	// Create HTTP server
	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		Engine: eng,
	})

	// Create test server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Test GET /executions/{id}
	resp, err := http.Get(ts.URL + "/executions/exec-1")
	assert.NoError(t, err)
	assert.Equal(t, resp.StatusCode, 200)

	var gotExec domain.ExecutionRecord
	err = json.NewDecoder(resp.Body).Decode(&gotExec)
	resp.Body.Close()
	assert.NoError(t, err)
	assert.Equal(t, gotExec.ID, "exec-1")
	assert.Equal(t, gotExec.WorkflowName, "test-workflow")

	// Test GET /executions
	resp, err = http.Get(ts.URL + "/executions")
	assert.NoError(t, err)
	assert.Equal(t, resp.StatusCode, 200)

	var executions []*domain.ExecutionRecord
	err = json.NewDecoder(resp.Body).Decode(&executions)
	resp.Body.Close()
	assert.NoError(t, err)
	assert.Len(t, executions, 1)
	assert.Equal(t, executions[0].ID, "exec-1")

	// Test POST /executions/{id}/cancel
	req, err := http.NewRequest("POST", ts.URL+"/executions/exec-1/cancel", nil)
	assert.NoError(t, err)
	resp, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, resp.StatusCode, 200)
	resp.Body.Close()

	// Verify cancelled
	resp, err = http.Get(ts.URL + "/executions/exec-1")
	assert.NoError(t, err)
	err = json.NewDecoder(resp.Body).Decode(&gotExec)
	resp.Body.Close()
	assert.NoError(t, err)
	assert.Equal(t, gotExec.Status, domain.ExecutionStatusCancelled)
}

// TestEngineWithHTTPServer tests full engine functionality with HTTP API.
func TestEngineWithHTTPServer(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a simple workflow
	wf, err := workflow.New(workflow.Options{
		Name: "test-workflow",
		Steps: []*workflow.Step{
			{
				Name:     "greet",
				Activity: "greeter",
			},
		},
	})
	assert.NoError(t, err)

	// Create engine with inline runner
	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store: store,
		Workflows: map[string]*workflow.Workflow{
			"test-workflow": wf,
		},
		Runners: map[string]domain.Runner{
			"greeter": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					return map[string]any{"greeting": "Hello!"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	// Start engine
	err = engine.Start(ctx)
	assert.NoError(t, err)

	// Submit workflow
	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)
	assert.NotEmpty(t, handle.ID)

	// Wait for completion with timeout
	deadline := time.Now().Add(5 * time.Second)
	var finalStatus domain.ExecutionStatus
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Shutdown engine
	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestHTTPServerHealthCheck tests the health endpoint.
func TestHTTPServerHealthCheck(t *testing.T) {
	// Create HTTP server with no services
	server := workflowhttp.NewServer(workflowhttp.ServerOptions{})

	// Create test server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Check health endpoint
	resp, err := ts.Client().Get(ts.URL + "/health")
	assert.NoError(t, err)
	assert.Equal(t, resp.StatusCode, 200)
}

// TestHTTPTaskClaimNoTasksAvailable tests claiming when no tasks exist.
func TestHTTPTaskClaimNoTasksAvailable(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create services (no tasks in store)
	taskService := services.NewTaskService(services.TaskServiceOptions{
		Tasks:  store,
		Events: store,
	})

	// Create HTTP server
	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		TaskService: taskService,
	})

	// Create test server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Create HTTP client
	client := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{
		BaseURL: ts.URL,
	})

	// Claim task should return nil (no tasks available)
	claimed, err := client.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.Nil(t, claimed)
}

// TestHTTPTaskCompleteWrongWorker tests completing a task with wrong worker ID.
func TestHTTPTaskCompleteWrongWorker(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a task and claim it
	task := &domain.TaskRecord{
		ID:           "task-1",
		ExecutionID:  "exec-1",
		StepName:     "step-1",
		ActivityName: "test-activity",
		Status:       domain.TaskStatusPending,
		Input:         &domain.TaskInput{Type: "inline"},
		CreatedAt:    time.Now(),
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim the task as worker-1
	claimed, err := store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed)

	// Create services
	taskService := services.NewTaskService(services.TaskServiceOptions{
		Tasks:  store,
		Events: store,
	})

	// Create HTTP server
	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		TaskService: taskService,
	})

	// Create test server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Create HTTP client
	client := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{
		BaseURL: ts.URL,
	})

	// Try to complete as different worker - should fail
	result := &domain.TaskOutput{Success: true}
	err = client.CompleteTask(ctx, claimed.ID, "worker-2", result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task not owned by this worker")
}

// TestHTTPServerWithAuth tests authentication middleware.
func TestHTTPServerWithAuth(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create task
	task := &domain.TaskRecord{
		ID:           "task-1",
		ExecutionID:  "exec-1",
		StepName:     "step-1",
		ActivityName: "test-activity",
		Status:       domain.TaskStatusPending,
		Input:         &domain.TaskInput{Type: "inline"},
		CreatedAt:    time.Now(),
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	taskService := services.NewTaskService(services.TaskServiceOptions{
		Tasks: store,
	})

	// Create server with token auth
	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		TaskService: taskService,
		Auth:        workflowhttp.NewTokenAuthenticator([]string{"secret-token"}),
	})

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Client without token should fail
	clientNoAuth := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{
		BaseURL: ts.URL,
	})
	_, err = clientNoAuth.ClaimTask(ctx, "worker-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "401")

	// Client with token should succeed
	clientWithAuth := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{
		BaseURL: ts.URL,
		Token:   "secret-token",
	})
	claimed, err := clientWithAuth.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed)
}

// TestHTTPServerListenAndServe tests actual network serving.
func TestHTTPServerListenAndServe(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	taskService := services.NewTaskService(services.TaskServiceOptions{
		Tasks: store,
	})

	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		TaskService: taskService,
	})

	// Get a free port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	addr := l.Addr().String()

	// Start server in background
	go func() {
		server.Serve(l)
	}()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Test health endpoint
	client := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{
		BaseURL: "http://" + addr,
	})

	// Claim with no tasks should return nil
	claimed, err := client.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.Nil(t, claimed)

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = server.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

// TestMultipleWorkersClaimTasks tests that multiple workers can claim different tasks.
func TestMultipleWorkersClaimTasks(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create multiple tasks
	for i := 1; i <= 3; i++ {
		task := &domain.TaskRecord{
			ID:           fmt.Sprintf("task-%d", i),
			ExecutionID:  fmt.Sprintf("exec-%d", i),
			StepName:     "step-1",
			ActivityName: "test-activity",
			Status:       domain.TaskStatusPending,
			Input:         &domain.TaskInput{Type: "inline"},
			CreatedAt:    time.Now(),
		}
		err := store.CreateTask(ctx, task)
		assert.NoError(t, err)
	}

	taskService := services.NewTaskService(services.TaskServiceOptions{
		Tasks:  store,
		Events: store,
	})

	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		TaskService: taskService,
	})

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Create multiple workers
	client1 := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{BaseURL: ts.URL})
	client2 := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{BaseURL: ts.URL})
	client3 := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{BaseURL: ts.URL})

	// Each worker claims a task
	claimed1, err := client1.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed1)

	claimed2, err := client2.ClaimTask(ctx, "worker-2")
	assert.NoError(t, err)
	assert.NotNil(t, claimed2)

	claimed3, err := client3.ClaimTask(ctx, "worker-3")
	assert.NoError(t, err)
	assert.NotNil(t, claimed3)

	// All should have claimed different tasks
	assert.NotEqual(t, claimed1.ID, claimed2.ID)
	assert.NotEqual(t, claimed2.ID, claimed3.ID)
	assert.NotEqual(t, claimed1.ID, claimed3.ID)

	// Fourth claim should return nil (no more tasks)
	claimed4, err := client1.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.Nil(t, claimed4)
}

// TestEngineSingleStepWorkflow tests a workflow with a single step.
// Note: The Engine currently only supports single-step workflows.
// Multi-step workflow support is TODO (see internal/engine/engine.go).
func TestEngineSingleStepWorkflow(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a single-step workflow
	wf, err := workflow.New(workflow.Options{
		Name: "single-step",
		Steps: []*workflow.Step{
			{
				Name:     "step1",
				Activity: "activity1",
				Store:    "result",
			},
		},
	})
	assert.NoError(t, err)

	activityCalled := false

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"single-step": wf},
		Runners: map[string]domain.Runner{
			"activity1": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activityCalled = true
					return map[string]any{"result": "success"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)
	assert.True(t, activityCalled)
	assert.Equal(t, finalRecord.Outputs["result"], "success")

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineMultiStepWorkflow tests a workflow with multiple steps.
func TestEngineMultiStepWorkflow(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a multi-step workflow: step1 -> step2 -> step3
	wf, err := workflow.New(workflow.Options{
		Name: "multi-step",
		Steps: []*workflow.Step{
			{
				Name:     "step1",
				Activity: "activity1",
				Next:     []*workflow.Edge{{Step: "step2"}},
			},
			{
				Name:     "step2",
				Activity: "activity2",
				Parameters: map[string]any{
					"input": "$(steps.step1.value)",
				},
				Next: []*workflow.Edge{{Step: "step3"}},
			},
			{
				Name:     "step3",
				Activity: "activity3",
				Parameters: map[string]any{
					"input": "$(steps.step2.value)",
				},
			},
		},
	})
	assert.NoError(t, err)

	// Track which activities were called and in what order
	var callOrder []string

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"multi-step": wf},
		Runners: map[string]domain.Runner{
			"activity1": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					callOrder = append(callOrder, "activity1")
					return map[string]any{"value": "from-step1"}, nil
				},
			},
			"activity2": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					callOrder = append(callOrder, "activity2")
					// Check that input from step1 was passed
					input, _ := params["input"].(string)
					return map[string]any{"value": "from-step2", "received": input}, nil
				},
			},
			"activity3": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					callOrder = append(callOrder, "activity3")
					// Check that input from step2 was passed
					input, _ := params["input"].(string)
					return map[string]any{"result": "done", "received": input}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify execution completed successfully
	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Verify all activities were called in order
	assert.Equal(t, len(callOrder), 3)
	assert.Equal(t, callOrder[0], "activity1")
	assert.Equal(t, callOrder[1], "activity2")
	assert.Equal(t, callOrder[2], "activity3")

	// Verify final outputs contain the last step's result
	assert.Equal(t, finalRecord.Outputs["result"], "done")

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineBranchingWorkflow tests a workflow with conditional branching.
// Note: Complex branching with conditions requires the full expression evaluator.
// This test uses a simpler unconditional branching pattern.
func TestEngineBranchingWorkflow(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a simpler branching workflow: start -> pathA (unconditional edge)
	wf, err := workflow.New(workflow.Options{
		Name: "branching",
		Steps: []*workflow.Step{
			{
				Name:     "start",
				Activity: "check-condition",
				Next: []*workflow.Edge{
					{Step: "pathA"}, // Unconditional edge to pathA
				},
			},
			{
				Name:     "pathA",
				Activity: "process-a",
			},
		},
	})
	assert.NoError(t, err)

	// Track which activities were called
	var activitiesCalled []string

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"branching": wf},
		Runners: map[string]domain.Runner{
			"check-condition": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "check-condition")
					return map[string]any{"route": "A"}, nil
				},
			},
			"process-a": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-a")
					return map[string]any{"result": "path-A-complete"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify execution completed successfully
	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Verify both activities were called
	assert.Equal(t, len(activitiesCalled), 2)
	assert.Equal(t, activitiesCalled[0], "check-condition")
	assert.Equal(t, activitiesCalled[1], "process-a")

	// Verify final output
	assert.Equal(t, finalRecord.Outputs["result"], "path-A-complete")

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineConditionalBranching tests a workflow with conditional edge evaluation.
func TestEngineConditionalBranching(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a workflow with conditional branching:
	// start -> (if route == 'A') pathA
	//       -> (if route == 'B') pathB
	wf, err := workflow.New(workflow.Options{
		Name: "conditional",
		Steps: []*workflow.Step{
			{
				Name:                 "start",
				Activity:             "decide-route",
				EdgeMatchingStrategy: workflow.EdgeMatchingFirst, // Take first matching edge
				Next: []*workflow.Edge{
					{Step: "pathA", Condition: "steps.start.route == 'A'"},
					{Step: "pathB", Condition: "steps.start.route == 'B'"},
				},
			},
			{
				Name:     "pathA",
				Activity: "process-a",
			},
			{
				Name:     "pathB",
				Activity: "process-b",
			},
		},
	})
	assert.NoError(t, err)

	// Track which activities were called
	var activitiesCalled []string

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"conditional": wf},
		Runners: map[string]domain.Runner{
			"decide-route": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "decide-route")
					// Return route 'A' to trigger pathA
					return map[string]any{"route": "A"}, nil
				},
			},
			"process-a": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-a")
					return map[string]any{"result": "took-path-A"}, nil
				},
			},
			"process-b": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-b")
					return map[string]any{"result": "took-path-B"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify execution completed successfully
	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Verify only pathA was taken (not pathB)
	assert.Equal(t, len(activitiesCalled), 2)
	assert.Equal(t, activitiesCalled[0], "decide-route")
	assert.Equal(t, activitiesCalled[1], "process-a")

	// Verify final output reflects pathA
	assert.Equal(t, finalRecord.Outputs["result"], "took-path-A")

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineConditionalBranchingPathB tests conditional branching taking the second path.
func TestEngineConditionalBranchingPathB(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	wf, err := workflow.New(workflow.Options{
		Name: "conditional-b",
		Steps: []*workflow.Step{
			{
				Name:                 "start",
				Activity:             "decide-route",
				EdgeMatchingStrategy: workflow.EdgeMatchingFirst,
				Next: []*workflow.Edge{
					{Step: "pathA", Condition: "steps.start.route == 'A'"},
					{Step: "pathB", Condition: "steps.start.route == 'B'"},
				},
			},
			{
				Name:     "pathA",
				Activity: "process-a",
			},
			{
				Name:     "pathB",
				Activity: "process-b",
			},
		},
	})
	assert.NoError(t, err)

	var activitiesCalled []string

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"conditional-b": wf},
		Runners: map[string]domain.Runner{
			"decide-route": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "decide-route")
					// Return route 'B' to trigger pathB
					return map[string]any{"route": "B"}, nil
				},
			},
			"process-a": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-a")
					return map[string]any{"result": "took-path-A"}, nil
				},
			},
			"process-b": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-b")
					return map[string]any{"result": "took-path-B"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Verify pathB was taken (not pathA)
	assert.Equal(t, len(activitiesCalled), 2)
	assert.Equal(t, activitiesCalled[0], "decide-route")
	assert.Equal(t, activitiesCalled[1], "process-b")

	assert.Equal(t, finalRecord.Outputs["result"], "took-path-B")

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineParallelBranching tests a workflow that forks into parallel paths.
func TestEngineParallelBranching(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a workflow that forks into two parallel paths
	// start -> (path: path-a) stepA
	//       -> (path: path-b) stepB
	wf, err := workflow.New(workflow.Options{
		Name: "parallel",
		Steps: []*workflow.Step{
			{
				Name:     "start",
				Activity: "init",
				Next: []*workflow.Edge{
					{Step: "stepA", Path: "path-a"},
					{Step: "stepB", Path: "path-b"},
				},
			},
			{
				Name:     "stepA",
				Activity: "process-a",
			},
			{
				Name:     "stepB",
				Activity: "process-b",
			},
		},
	})
	assert.NoError(t, err)

	var activitiesCalled []string

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"parallel": wf},
		Runners: map[string]domain.Runner{
			"init": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "init")
					return map[string]any{"started": true}, nil
				},
			},
			"process-a": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-a")
					return map[string]any{"result": "A-done"}, nil
				},
			},
			"process-b": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-b")
					return map[string]any{"result": "B-done"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1, // Sequential to avoid race conditions
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Verify all activities were called (init + both parallel branches)
	assert.Equal(t, len(activitiesCalled), 3)
	assert.Equal(t, activitiesCalled[0], "init")

	// The order of parallel branches may vary, so check both are present
	hasA := false
	hasB := false
	for _, a := range activitiesCalled {
		if a == "process-a" {
			hasA = true
		}
		if a == "process-b" {
			hasB = true
		}
	}
	assert.True(t, hasA)
	assert.True(t, hasB)

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineJoinWorkflow tests a workflow with parallel paths joining.
func TestEngineJoinWorkflow(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a workflow that forks and joins:
	// start -> (path: path-a) stepA -> join
	//       -> (path: path-b) stepB -> join
	wf, err := workflow.New(workflow.Options{
		Name: "join-test",
		Steps: []*workflow.Step{
			{
				Name:     "start",
				Activity: "init",
				Next: []*workflow.Edge{
					{Step: "stepA", Path: "path-a"},
					{Step: "stepB", Path: "path-b"},
				},
			},
			{
				Name:     "stepA",
				Activity: "process-a",
				Next:     []*workflow.Edge{{Step: "join"}},
			},
			{
				Name:     "stepB",
				Activity: "process-b",
				Next:     []*workflow.Edge{{Step: "join"}},
			},
			{
				Name:     "join",
				Activity: "finalize",
				Join: &workflow.JoinConfig{
					Paths: []string{"path-a", "path-b"},
				},
				Parameters: map[string]any{
					// Access outputs from joined paths
					"resultA": "$(path.path-a.steps.stepA.resultA)",
					"resultB": "$(path.path-b.steps.stepB.resultB)",
				},
			},
		},
	})
	assert.NoError(t, err)

	var activitiesCalled []string
	var finalizeParams map[string]any

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"join-test": wf},
		Runners: map[string]domain.Runner{
			"init": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "init")
					return map[string]any{"started": true}, nil
				},
			},
			"process-a": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-a")
					return map[string]any{"resultA": "value-from-A"}, nil
				},
			},
			"process-b": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "process-b")
					return map[string]any{"resultB": "value-from-B"}, nil
				},
			},
			"finalize": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "finalize")
					finalizeParams = params
					return map[string]any{"combined": "done"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1, // Sequential to avoid race conditions
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Verify all activities were called (init, parallel branches, finalize)
	assert.Equal(t, len(activitiesCalled), 4)
	assert.Equal(t, activitiesCalled[0], "init")
	assert.Equal(t, activitiesCalled[len(activitiesCalled)-1], "finalize")

	// Verify finalize received parameters from both paths
	assert.Equal(t, finalizeParams["resultA"], "value-from-A")
	assert.Equal(t, finalizeParams["resultB"], "value-from-B")

	// Verify final output
	assert.Equal(t, finalRecord.Outputs["combined"], "done")

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineNumericCondition tests conditional branching with numeric comparisons.
func TestEngineNumericCondition(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a workflow with numeric condition:
	// start -> (if count > 5) big
	//       -> (if count <= 5) small
	wf, err := workflow.New(workflow.Options{
		Name: "numeric-condition",
		Steps: []*workflow.Step{
			{
				Name:                 "start",
				Activity:             "get-count",
				EdgeMatchingStrategy: workflow.EdgeMatchingFirst,
				Next: []*workflow.Edge{
					{Step: "big", Condition: "steps.start.count > 5"},
					{Step: "small", Condition: "steps.start.count <= 5"},
				},
			},
			{
				Name:     "big",
				Activity: "handle-big",
			},
			{
				Name:     "small",
				Activity: "handle-small",
			},
		},
	})
	assert.NoError(t, err)

	var activitiesCalled []string

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"numeric-condition": wf},
		Runners: map[string]domain.Runner{
			"get-count": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "get-count")
					return map[string]any{"count": 10}, nil // count > 5
				},
			},
			"handle-big": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "handle-big")
					return map[string]any{"result": "big-path"}, nil
				},
			},
			"handle-small": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					activitiesCalled = append(activitiesCalled, "handle-small")
					return map[string]any{"result": "small-path"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)

	// Verify big path was taken (count=10 > 5)
	assert.Equal(t, len(activitiesCalled), 2)
	assert.Equal(t, activitiesCalled[0], "get-count")
	assert.Equal(t, activitiesCalled[1], "handle-big")

	assert.Equal(t, finalRecord.Outputs["result"], "big-path")

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineRetryOnFailure tests that failed tasks are retried according to retry config.
func TestEngineRetryOnFailure(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a workflow with retry configuration
	wf, err := workflow.New(workflow.Options{
		Name: "retry-test",
		Steps: []*workflow.Step{
			{
				Name:     "flaky-step",
				Activity: "flaky-activity",
				Retry: []*workflow.RetryConfig{
					{
						MaxRetries:  3,
						BaseDelay:   10 * time.Millisecond,
						BackoffRate: 1.0, // No exponential backoff for faster test
					},
				},
			},
		},
	})
	assert.NoError(t, err)

	// Track attempt count
	attemptCount := 0

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"retry-test": wf},
		Runners: map[string]domain.Runner{
			"flaky-activity": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					attemptCount++
					// Fail on first 2 attempts, succeed on 3rd
					if attemptCount < 3 {
						return nil, fmt.Errorf("transient error attempt %d", attemptCount)
					}
					return map[string]any{"result": "success"}, nil
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	var finalRecord *domain.ExecutionRecord
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		finalRecord = record
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify execution completed successfully after retries
	assert.Equal(t, finalStatus, domain.ExecutionStatusCompleted)
	assert.Equal(t, finalRecord.Outputs["result"], "success")

	// Verify we had 3 attempts (initial + 2 retries)
	assert.Equal(t, attemptCount, 3)

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineMaxRetriesExceeded tests that execution fails when max retries are exceeded.
func TestEngineMaxRetriesExceeded(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a workflow with limited retries
	wf, err := workflow.New(workflow.Options{
		Name: "max-retry-test",
		Steps: []*workflow.Step{
			{
				Name:     "always-fails",
				Activity: "failing-activity",
				Retry: []*workflow.RetryConfig{
					{
						MaxRetries:  2,
						BaseDelay:   10 * time.Millisecond,
						BackoffRate: 1.0,
					},
				},
			},
		},
	})
	assert.NoError(t, err)

	attemptCount := 0

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"max-retry-test": wf},
		Runners: map[string]domain.Runner{
			"failing-activity": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					attemptCount++
					return nil, fmt.Errorf("permanent error")
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus domain.ExecutionStatus
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify execution failed after max retries
	assert.Equal(t, finalStatus, domain.ExecutionStatusFailed)

	// Verify we had 3 attempts (initial + 2 retries = 3)
	assert.Equal(t, attemptCount, 3)

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestEngineNoRetryWithoutConfig tests that tasks without retry config fail immediately.
func TestEngineNoRetryWithoutConfig(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create a workflow WITHOUT retry configuration
	wf, err := workflow.New(workflow.Options{
		Name: "no-retry-test",
		Steps: []*workflow.Step{
			{
				Name:     "no-retry-step",
				Activity: "failing-activity",
				// No Retry config
			},
		},
	})
	assert.NoError(t, err)

	attemptCount := 0

	engine, err := workflow.NewEngine(workflow.EngineOptions{
		Store:     store,
		Workflows: map[string]*workflow.Workflow{"no-retry-test": wf},
		Runners: map[string]domain.Runner{
			"failing-activity": &runners.InlineRunner{
				Func: func(ctx context.Context, params map[string]any) (map[string]any, error) {
					attemptCount++
					return nil, fmt.Errorf("error")
				},
			},
		},
		WorkerID:      "test-engine",
		MaxConcurrent: 1,
		PollInterval:  50 * time.Millisecond,
	})
	assert.NoError(t, err)

	err = engine.Start(ctx)
	assert.NoError(t, err)

	handle, err := engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: wf,
		Inputs:   map[string]any{},
	})
	assert.NoError(t, err)

	// Wait for completion
	deadline := time.Now().Add(5 * time.Second)
	var finalStatus domain.ExecutionStatus
	for time.Now().Before(deadline) {
		record, err := engine.Get(ctx, handle.ID)
		assert.NoError(t, err)
		finalStatus = record.Status
		if finalStatus == domain.ExecutionStatusCompleted || finalStatus == domain.ExecutionStatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify execution failed immediately without retry
	assert.Equal(t, finalStatus, domain.ExecutionStatusFailed)

	// Verify only 1 attempt was made
	assert.Equal(t, attemptCount, 1)

	err = engine.Shutdown(ctx)
	assert.NoError(t, err)
}

// Unused helper imports - keeping for potential future use
var (
	_ = bytes.Buffer{}
	_ = io.EOF
)
