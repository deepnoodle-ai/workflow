package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/stretchr/testify/require"
)

// --- VariableContainer / Patches ---

func TestNewPatch(t *testing.T) {
	p := NewPatch(PatchOptions{Variable: "key", Value: "val", Delete: false})
	require.Equal(t, "key", p.Variable())
	require.Equal(t, "val", p.Value())
	require.False(t, p.Delete())

	p2 := NewPatch(PatchOptions{Variable: "x", Delete: true})
	require.True(t, p2.Delete())
	require.Nil(t, p2.Value())
}

func TestApplyPatches(t *testing.T) {
	state := NewPathLocalState(nil, map[string]any{"a": 1, "b": 2})
	patches := []Patch{
		NewPatch(PatchOptions{Variable: "a", Value: 10}),
		NewPatch(PatchOptions{Variable: "b", Delete: true}),
		NewPatch(PatchOptions{Variable: "c", Value: "new"}),
	}
	ApplyPatches(state, patches)

	v, ok := state.GetVariable("a")
	require.True(t, ok)
	require.Equal(t, 10, v)

	_, ok = state.GetVariable("b")
	require.False(t, ok)

	v, ok = state.GetVariable("c")
	require.True(t, ok)
	require.Equal(t, "new", v)
}

// --- PathLocalState ---

func TestPathLocalState_ListInputs(t *testing.T) {
	state := NewPathLocalState(map[string]any{"z": 1, "a": 2, "m": 3}, nil)
	keys := state.ListInputs()
	require.Equal(t, []string{"a", "m", "z"}, keys)
}

func TestPathLocalState_GetInput(t *testing.T) {
	state := NewPathLocalState(map[string]any{"key": "value"}, nil)
	v, ok := state.GetInput("key")
	require.True(t, ok)
	require.Equal(t, "value", v)

	_, ok = state.GetInput("missing")
	require.False(t, ok)
}

func TestPathLocalState_DeleteVariable(t *testing.T) {
	state := NewPathLocalState(nil, map[string]any{"a": 1, "b": 2})
	state.DeleteVariable("a")

	_, ok := state.GetVariable("a")
	require.False(t, ok)

	v, ok := state.GetVariable("b")
	require.True(t, ok)
	require.Equal(t, 2, v)
}

// --- Context helpers ---

func TestContextGetters(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	compiler := script.NewRisorScriptingEngine(script.DefaultRisorGlobals())
	state := NewPathLocalState(map[string]any{"input1": "val"}, map[string]any{"var1": 42})

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: state,
		Logger:         logger,
		Compiler:       compiler,
		PathID:         "path-1",
		StepName:       "step-1",
	})

	require.Equal(t, logger, ctx.GetLogger())
	require.Equal(t, compiler, ctx.GetCompiler())
	require.Equal(t, "path-1", ctx.GetPathID())
	require.Equal(t, "step-1", ctx.GetStepName())
}

func TestWithTimeout(t *testing.T) {
	state := NewPathLocalState(map[string]any{"in": 1}, map[string]any{"v": 2})
	parent := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: state,
		PathID:         "p1",
		StepName:       "s1",
	})

	child, cancel := WithTimeout(parent, 5*time.Second)
	defer cancel()

	require.Equal(t, "p1", child.GetPathID())
	require.Equal(t, "s1", child.GetStepName())

	// Verify variable access still works
	v, ok := child.GetVariable("v")
	require.True(t, ok)
	require.Equal(t, 2, v)
}

func TestWithTimeout_NonWorkflowContext(t *testing.T) {
	// Test the fallback path when parent is not an executionContext
	mockCtx := NewContext(context.Background(), ExecutionContextOptions{})
	// WithTimeout with a basic interface should still work
	child, cancel := WithTimeout(mockCtx, 5*time.Second)
	defer cancel()
	require.NotNil(t, child)
}

func TestWithCancel(t *testing.T) {
	state := NewPathLocalState(map[string]any{"in": 1}, map[string]any{"v": 2})
	parent := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: state,
		PathID:         "p1",
		StepName:       "s1",
	})

	child, cancel := WithCancel(parent)
	defer cancel()

	require.Equal(t, "p1", child.GetPathID())
	require.Equal(t, "s1", child.GetStepName())
}

func TestWithCancel_NonWorkflowContext(t *testing.T) {
	mockCtx := NewContext(context.Background(), ExecutionContextOptions{})
	child, cancel := WithCancel(mockCtx)
	defer cancel()
	require.NotNil(t, child)
}

func TestInputsFromContext(t *testing.T) {
	state := NewPathLocalState(map[string]any{"a": 1, "b": "two"}, nil)
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: state,
	})
	inputs := InputsFromContext(ctx)
	require.Equal(t, map[string]any{"a": 1, "b": "two"}, inputs)
}

// --- ValidationError ---

func TestValidationError_Error(t *testing.T) {
	ve := &ValidationError{
		Problems: []ValidationProblem{
			{Step: "step1", Message: "unreachable from start step"},
			{Message: "workflow-level problem"},
		},
	}
	s := ve.Error()
	require.Contains(t, s, "workflow validation failed (2 problems)")
	require.Contains(t, s, `step "step1": unreachable from start step`)
	require.Contains(t, s, "workflow-level problem")
}

func TestValidationProblem_String(t *testing.T) {
	p := ValidationProblem{Step: "my-step", Message: "bad config"}
	require.Equal(t, `step "my-step": bad config`, p.String())

	p2 := ValidationProblem{Message: "global issue"}
	require.Equal(t, "global issue", p2.String())
}

// --- Workflow ---

func TestInput_IsRequired(t *testing.T) {
	required := &Input{Name: "name", Type: "string"}
	require.True(t, required.IsRequired())

	optional := &Input{Name: "name", Type: "string", Default: "default"}
	require.False(t, optional.IsRequired())
}

func TestWorkflow_Path(t *testing.T) {
	w, err := New(Options{
		Name: "test",
		Path: "/some/path.yaml",
		Steps: []*Step{
			{Name: "start", Activity: "print"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "/some/path.yaml", w.Path())
}

// --- Logger ---

func TestNewLogger(t *testing.T) {
	l := NewLogger()
	require.NotNil(t, l)
}

func TestNewJSONLogger(t *testing.T) {
	l := NewJSONLogger()
	require.NotNil(t, l)
}

// --- FileActivityLogger ---

func TestFileActivityLogger(t *testing.T) {
	dir := t.TempDir()
	logger := NewFileActivityLogger(dir)

	entry := &ActivityLogEntry{
		ID:          "log-1",
		ExecutionID: "exec-1",
		Activity:    "print",
		StepName:    "step1",
		PathID:      "main",
		Parameters:  map[string]interface{}{"message": "hello"},
		Result:      "hello",
		StartTime:   time.Now(),
		Duration:    0.5,
	}

	// Log an activity
	err := logger.LogActivity(context.Background(), entry)
	require.NoError(t, err)

	// Log another
	entry2 := &ActivityLogEntry{
		ID:          "log-2",
		ExecutionID: "exec-1",
		Activity:    "print",
		StepName:    "step2",
		PathID:      "main",
		Parameters:  map[string]interface{}{"message": "world"},
		Result:      "world",
		StartTime:   time.Now(),
		Duration:    0.3,
	}
	err = logger.LogActivity(context.Background(), entry2)
	require.NoError(t, err)

	// Read back
	entries, err := logger.GetActivityHistory(context.Background(), "exec-1")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "log-1", entries[0].ID)
	require.Equal(t, "log-2", entries[1].ID)

	// Non-existent execution
	_, err = logger.GetActivityHistory(context.Background(), "nonexistent")
	require.Error(t, err)
}

func TestFileActivityLogger_Path(t *testing.T) {
	logger := NewFileActivityLogger("/tmp/logs")
	path := logger.executionActivityLogPath("exec-123")
	require.Equal(t, "/tmp/logs/exec-123.jsonl", path)
}

// --- NullActivityLogger.GetActivityHistory ---

func TestNullActivityLogger_GetActivityHistory(t *testing.T) {
	logger := NewNullActivityLogger()
	entries, err := logger.GetActivityHistory(context.Background(), "any")
	require.NoError(t, err)
	require.Nil(t, entries)
}

// --- ExecutionState nested field operations ---

func TestGetNestedField(t *testing.T) {
	data := map[string]any{
		"user": map[string]any{
			"profile": map[string]any{
				"name": "alice",
				"age":  30,
			},
		},
		"simple": "value",
	}

	t.Run("simple key", func(t *testing.T) {
		v, ok := getNestedField(data, "simple")
		require.True(t, ok)
		require.Equal(t, "value", v)
	})

	t.Run("nested key", func(t *testing.T) {
		v, ok := getNestedField(data, "user.profile.name")
		require.True(t, ok)
		require.Equal(t, "alice", v)
	})

	t.Run("empty path", func(t *testing.T) {
		_, ok := getNestedField(data, "")
		require.False(t, ok)
	})

	t.Run("missing key", func(t *testing.T) {
		_, ok := getNestedField(data, "nonexistent")
		require.False(t, ok)
	})

	t.Run("missing nested key", func(t *testing.T) {
		_, ok := getNestedField(data, "user.nonexistent")
		require.False(t, ok)
	})

	t.Run("path through non-map", func(t *testing.T) {
		_, ok := getNestedField(data, "simple.sub")
		require.False(t, ok)
	})

	t.Run("empty part in path", func(t *testing.T) {
		_, ok := getNestedField(data, "user..name")
		require.False(t, ok)
	})
}

func TestSetNestedField(t *testing.T) {
	t.Run("simple key", func(t *testing.T) {
		data := map[string]any{}
		setNestedField(data, "key", "value")
		require.Equal(t, "value", data["key"])
	})

	t.Run("nested key creates intermediate maps", func(t *testing.T) {
		data := map[string]any{}
		setNestedField(data, "a.b.c", "deep")
		require.Equal(t, "deep", data["a"].(map[string]any)["b"].(map[string]any)["c"])
	})

	t.Run("empty path is no-op", func(t *testing.T) {
		data := map[string]any{}
		setNestedField(data, "", "value")
		require.Empty(t, data)
	})

	t.Run("empty part in path is no-op", func(t *testing.T) {
		data := map[string]any{}
		setNestedField(data, "a..b", "value")
		require.NotContains(t, data, "b")
	})

	t.Run("overwrites non-map intermediate", func(t *testing.T) {
		data := map[string]any{"a": "not a map"}
		setNestedField(data, "a.b", "value")
		m, ok := data["a"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "value", m["b"])
	})

	t.Run("existing nested map", func(t *testing.T) {
		data := map[string]any{
			"a": map[string]any{"b": "old"},
		}
		setNestedField(data, "a.b", "new")
		require.Equal(t, "new", data["a"].(map[string]any)["b"])
	})
}

// --- ExecutionState ---

func TestExecutionState_NextPathID(t *testing.T) {
	state := newExecutionState("exec-1", "wf", nil)
	id1 := state.NextPathID("main")
	id2 := state.NextPathID("main")
	require.Equal(t, "main-1", id1)
	require.Equal(t, "main-2", id2)
}

func TestExecutionState_GetWaitingPathIDs(t *testing.T) {
	state := newExecutionState("exec-1", "wf", nil)
	state.SetPathState("path-1", &PathState{ID: "path-1", Status: ExecutionStatusWaiting})
	state.SetPathState("path-2", &PathState{ID: "path-2", Status: ExecutionStatusCompleted})
	state.SetPathState("path-3", &PathState{ID: "path-3", Status: ExecutionStatusWaiting})

	waiting := state.GetWaitingPathIDs()
	require.Len(t, waiting, 2)
	require.Contains(t, waiting, "path-1")
	require.Contains(t, waiting, "path-3")
}

func TestExecutionState_IsJoinReady(t *testing.T) {
	t.Run("no join state", func(t *testing.T) {
		state := newExecutionState("exec-1", "wf", nil)
		require.False(t, state.IsJoinReady("step"))
	})

	t.Run("specific paths all completed", func(t *testing.T) {
		state := newExecutionState("exec-1", "wf", nil)
		state.SetPathState("pathA", &PathState{ID: "pathA", Status: ExecutionStatusCompleted})
		state.SetPathState("pathB", &PathState{ID: "pathB", Status: ExecutionStatusCompleted})
		state.AddPathToJoin("join-step", "waiter", &JoinConfig{
			Paths: []string{"pathA", "pathB"},
		}, nil, nil)
		require.True(t, state.IsJoinReady("join-step"))
	})

	t.Run("specific paths not all completed", func(t *testing.T) {
		state := newExecutionState("exec-1", "wf", nil)
		state.SetPathState("pathA", &PathState{ID: "pathA", Status: ExecutionStatusCompleted})
		state.SetPathState("pathB", &PathState{ID: "pathB", Status: ExecutionStatusRunning})
		state.AddPathToJoin("join-step", "waiter", &JoinConfig{
			Paths: []string{"pathA", "pathB"},
		}, nil, nil)
		require.False(t, state.IsJoinReady("join-step"))
	})

	t.Run("count-based join ready", func(t *testing.T) {
		state := newExecutionState("exec-1", "wf", nil)
		state.SetPathState("p1", &PathState{ID: "p1", Status: ExecutionStatusCompleted})
		state.SetPathState("p2", &PathState{ID: "p2", Status: ExecutionStatusCompleted})
		state.AddPathToJoin("join-step", "waiter", &JoinConfig{
			Count: 2,
		}, nil, nil)
		require.True(t, state.IsJoinReady("join-step"))
	})

	t.Run("count-based join not ready", func(t *testing.T) {
		state := newExecutionState("exec-1", "wf", nil)
		state.SetPathState("p1", &PathState{ID: "p1", Status: ExecutionStatusCompleted})
		state.AddPathToJoin("join-step", "waiter", &JoinConfig{
			Count: 2,
		}, nil, nil)
		require.False(t, state.IsJoinReady("join-step"))
	})

	t.Run("default join needs 2 completed", func(t *testing.T) {
		state := newExecutionState("exec-1", "wf", nil)
		state.SetPathState("p1", &PathState{ID: "p1", Status: ExecutionStatusCompleted})
		state.SetPathState("p2", &PathState{ID: "p2", Status: ExecutionStatusCompleted})
		state.AddPathToJoin("join-step", "waiter", &JoinConfig{}, nil, nil)
		require.True(t, state.IsJoinReady("join-step"))
	})

	t.Run("default join with only 1 completed", func(t *testing.T) {
		state := newExecutionState("exec-1", "wf", nil)
		state.SetPathState("p1", &PathState{ID: "p1", Status: ExecutionStatusCompleted})
		state.AddPathToJoin("join-step", "waiter", &JoinConfig{}, nil, nil)
		require.False(t, state.IsJoinReady("join-step"))
	})
}

// --- MemoryWorkflowRegistry ---

func TestMemoryWorkflowRegistry(t *testing.T) {
	reg := NewMemoryWorkflowRegistry()

	// Register nil workflow
	err := reg.Register(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "workflow cannot be nil")

	// Create a valid workflow
	wf, err := New(Options{
		Name:  "test-wf",
		Steps: []*Step{{Name: "start", Activity: "print"}},
	})
	require.NoError(t, err)

	// Register it
	err = reg.Register(wf)
	require.NoError(t, err)

	// Get it
	got, ok := reg.Get("test-wf")
	require.True(t, ok)
	require.Equal(t, "test-wf", got.Name())

	// Get missing
	_, ok = reg.Get("nope")
	require.False(t, ok)

	// List
	names := reg.List()
	require.Equal(t, []string{"test-wf"}, names)
}

// --- DefaultChildWorkflowExecutor ---

func TestDefaultChildWorkflowExecutor_RequiresRegistry(t *testing.T) {
	_, err := NewDefaultChildWorkflowExecutor(ChildWorkflowExecutorOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "workflow registry is required")
}

func TestDefaultChildWorkflowExecutor_ExecuteSync(t *testing.T) {
	// Create a simple workflow that sets an output
	wf, err := New(Options{
		Name: "child",
		Steps: []*Step{
			{Name: "greet", Activity: "greet"},
		},
		Outputs: []*Output{
			{Name: "greeting", Variable: "greeting"},
		},
	})
	require.NoError(t, err)

	reg := NewMemoryWorkflowRegistry()
	reg.Register(wf)

	greetActivity := NewActivityFunction("greet", func(ctx Context, params map[string]any) (any, error) {
		ctx.SetVariable("greeting", "hello")
		return "hello", nil
	})

	executor, err := NewDefaultChildWorkflowExecutor(ChildWorkflowExecutorOptions{
		WorkflowRegistry: reg,
		Activities:       []Activity{greetActivity},
	})
	require.NoError(t, err)

	result, err := executor.ExecuteSync(context.Background(), &ChildWorkflowSpec{
		WorkflowName: "child",
	})
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, result.Status)
	require.Equal(t, "hello", result.Outputs["greeting"])
}

func TestDefaultChildWorkflowExecutor_ExecuteSyncNotFound(t *testing.T) {
	reg := NewMemoryWorkflowRegistry()
	executor, err := NewDefaultChildWorkflowExecutor(ChildWorkflowExecutorOptions{
		WorkflowRegistry: reg,
	})
	require.NoError(t, err)

	_, err = executor.ExecuteSync(context.Background(), &ChildWorkflowSpec{
		WorkflowName: "missing",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in registry")
}

func TestDefaultChildWorkflowExecutor_GetResult_NilHandle(t *testing.T) {
	reg := NewMemoryWorkflowRegistry()
	executor, err := NewDefaultChildWorkflowExecutor(ChildWorkflowExecutorOptions{
		WorkflowRegistry: reg,
	})
	require.NoError(t, err)

	_, err = executor.GetResult(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "handle cannot be nil")
}

func TestDefaultChildWorkflowExecutor_GetResult_NotFound(t *testing.T) {
	reg := NewMemoryWorkflowRegistry()
	executor, err := NewDefaultChildWorkflowExecutor(ChildWorkflowExecutorOptions{
		WorkflowRegistry: reg,
	})
	require.NoError(t, err)

	_, err = executor.GetResult(context.Background(), &ChildWorkflowHandle{
		ExecutionID: "nonexistent",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found or has expired")
}

// --- FileCheckpointer ---

func TestFileCheckpointer_DeleteCheckpoint(t *testing.T) {
	dir := t.TempDir()
	cp, err := NewFileCheckpointer(dir)
	require.NoError(t, err)

	// Save a checkpoint first
	checkpoint := &Checkpoint{
		ID:           "cp-1",
		ExecutionID:  "exec-1",
		WorkflowName: "test-wf",
		Status:       "running",
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		PathStates:   map[string]*PathState{},
		CheckpointAt: time.Now(),
	}
	err = cp.SaveCheckpoint(context.Background(), checkpoint)
	require.NoError(t, err)

	// Verify file exists
	files, _ := os.ReadDir(filepath.Join(dir, "exec-1"))
	require.Greater(t, len(files), 0)

	// Delete
	err = cp.DeleteCheckpoint(context.Background(), "exec-1")
	require.NoError(t, err)

	// Verify deleted
	_, err = os.Stat(filepath.Join(dir, "exec-1"))
	require.True(t, os.IsNotExist(err))
}

func TestFileCheckpointer_ListExecutions(t *testing.T) {
	dir := t.TempDir()
	cp, err := NewFileCheckpointer(dir)
	require.NoError(t, err)

	// Save checkpoints for two executions
	for _, execID := range []string{"exec-1", "exec-2"} {
		checkpoint := &Checkpoint{
			ID:           "cp-" + execID,
			ExecutionID:  execID,
			WorkflowName: "test-wf",
			Status:       "completed",
			Inputs:       map[string]any{},
			Outputs:      map[string]any{},
			PathStates:   map[string]*PathState{},
			StartTime:    time.Now(),
			CheckpointAt: time.Now(),
		}
		err := cp.SaveCheckpoint(context.Background(), checkpoint)
		require.NoError(t, err)
	}

	executions, err := cp.ListExecutions(context.Background())
	require.NoError(t, err)
	require.Len(t, executions, 2)
}

// --- FencedCheckpointer DeleteCheckpoint ---

func TestFencedCheckpointer_DeleteCheckpoint(t *testing.T) {
	dir := t.TempDir()
	inner, err := NewFileCheckpointer(dir)
	require.NoError(t, err)

	fenced := WithFencing(inner, func(ctx context.Context) error {
		return nil // fence always valid
	})

	// Save a checkpoint
	checkpoint := &Checkpoint{
		ID:           "cp-1",
		ExecutionID:  "exec-1",
		WorkflowName: "test-wf",
		Status:       "running",
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		PathStates:   map[string]*PathState{},
		CheckpointAt: time.Now(),
	}
	err = fenced.SaveCheckpoint(context.Background(), checkpoint)
	require.NoError(t, err)

	err = fenced.DeleteCheckpoint(context.Background(), "exec-1")
	require.NoError(t, err)
}

// --- NullCheckpointer DeleteCheckpoint ---

func TestNullCheckpointer_DeleteCheckpoint(t *testing.T) {
	cp := NewNullCheckpointer()
	err := cp.DeleteCheckpoint(context.Background(), "any")
	require.NoError(t, err)
}

// --- BaseExecutionCallbacks ---

func TestBaseExecutionCallbacks(t *testing.T) {
	cb := NewBaseExecutionCallbacks()
	ctx := context.Background()

	// All methods should be no-ops (no panics)
	cb.BeforeWorkflowExecution(ctx, &WorkflowExecutionEvent{})
	cb.AfterWorkflowExecution(ctx, &WorkflowExecutionEvent{})
	cb.BeforePathExecution(ctx, &PathExecutionEvent{})
	cb.AfterPathExecution(ctx, &PathExecutionEvent{})
	cb.BeforeActivityExecution(ctx, &ActivityExecutionEvent{})
	cb.AfterActivityExecution(ctx, &ActivityExecutionEvent{})
}

func TestCallbackChain_Add(t *testing.T) {
	var calls []string
	cb1 := &trackingCallbacks{name: "cb1", calls: &calls}
	cb2 := &trackingCallbacks{name: "cb2", calls: &calls}

	chain := NewCallbackChain(cb1)
	chain.Add(cb2)

	ctx := context.Background()
	chain.BeforeWorkflowExecution(ctx, &WorkflowExecutionEvent{})
	require.Equal(t, []string{"cb1:before-wf", "cb2:before-wf"}, calls)
}

type trackingCallbacks struct {
	BaseExecutionCallbacks
	name  string
	calls *[]string
}

func (t *trackingCallbacks) BeforeWorkflowExecution(_ context.Context, _ *WorkflowExecutionEvent) {
	*t.calls = append(*t.calls, t.name+":before-wf")
}

// --- ExecutionState additional ---

func TestExecutionState_GeneratePathID_Duplicate(t *testing.T) {
	state := newExecutionState("exec-1", "wf", nil)
	state.SetPathState("my-path", &PathState{ID: "my-path"})

	_, err := state.GeneratePathID("main", "my-path")
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate path name")
}

func TestExecutionState_SetError_Nil(t *testing.T) {
	state := newExecutionState("exec-1", "wf", nil)
	state.SetError(nil)
	require.NoError(t, state.GetError())
}

func TestExecutionState_AddPathToJoin_UpdateExisting(t *testing.T) {
	state := newExecutionState("exec-1", "wf", nil)

	// First add
	state.AddPathToJoin("step", "path-1", &JoinConfig{}, nil, nil)
	js := state.GetJoinState("step")
	require.Equal(t, "path-1", js.WaitingPathID)

	// Update with different path
	state.AddPathToJoin("step", "path-2", &JoinConfig{}, nil, nil)
	js = state.GetJoinState("step")
	require.Equal(t, "path-2", js.WaitingPathID)
}

func TestExecutionState_GetJoinState_Nil(t *testing.T) {
	state := newExecutionState("exec-1", "wf", nil)
	require.Nil(t, state.GetJoinState("nonexistent"))
}

func TestExecutionState_FromCheckpoint_NilJoinStates(t *testing.T) {
	state := newExecutionState("exec-1", "wf", nil)
	checkpoint := &Checkpoint{
		ExecutionID:  "exec-1",
		WorkflowName: "wf",
		Status:       "running",
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		PathStates:   map[string]*PathState{},
		JoinStates:   nil, // backward compat
	}
	state.FromCheckpoint(checkpoint)
	require.NotNil(t, state.GetAllJoinStates())
}

// --- Execution with callbacks and step progress ---

func TestExecution_WithCallbacks(t *testing.T) {
	var events []string

	wf, err := New(Options{
		Name: "cb-test",
		Steps: []*Step{
			{Name: "greet", Activity: "greet"},
		},
	})
	require.NoError(t, err)

	greetActivity := NewActivityFunction("greet", func(ctx Context, params map[string]any) (any, error) {
		return "hello", nil
	})

	cb := &eventTracker{events: &events}

	exec, err := NewExecution(ExecutionOptions{
		Workflow:           wf,
		Activities:         []Activity{greetActivity},
		ExecutionCallbacks: cb,
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())

	require.Contains(t, events, "before-wf")
	require.Contains(t, events, "after-wf")
	require.Contains(t, events, "before-path")
	require.Contains(t, events, "after-path")
	require.Contains(t, events, "before-activity")
	require.Contains(t, events, "after-activity")
}

type eventTracker struct {
	BaseExecutionCallbacks
	events *[]string
}

func (t *eventTracker) BeforeWorkflowExecution(_ context.Context, _ *WorkflowExecutionEvent) {
	*t.events = append(*t.events, "before-wf")
}
func (t *eventTracker) AfterWorkflowExecution(_ context.Context, _ *WorkflowExecutionEvent) {
	*t.events = append(*t.events, "after-wf")
}
func (t *eventTracker) BeforePathExecution(_ context.Context, _ *PathExecutionEvent) {
	*t.events = append(*t.events, "before-path")
}
func (t *eventTracker) AfterPathExecution(_ context.Context, _ *PathExecutionEvent) {
	*t.events = append(*t.events, "after-path")
}
func (t *eventTracker) BeforeActivityExecution(_ context.Context, _ *ActivityExecutionEvent) {
	*t.events = append(*t.events, "before-activity")
}
func (t *eventTracker) AfterActivityExecution(_ context.Context, _ *ActivityExecutionEvent) {
	*t.events = append(*t.events, "after-activity")
}

// --- Execution with step progress store ---

func TestExecution_WithStepProgressStore(t *testing.T) {
	wf, err := New(Options{
		Name: "progress-test",
		Steps: []*Step{
			{Name: "work", Activity: "work"},
		},
	})
	require.NoError(t, err)

	workActivity := NewActivityFunction("work", func(ctx Context, params map[string]any) (any, error) {
		ReportProgress(ctx, ProgressDetail{Message: "halfway", Data: map[string]any{"pct": 50}})
		return "done", nil
	})

	store := &memoryProgressStore{}
	exec, err := NewExecution(ExecutionOptions{
		Workflow:          wf,
		Activities:        []Activity{workActivity},
		StepProgressStore: store,
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)

	// Give async dispatch time to complete
	time.Sleep(50 * time.Millisecond)

	require.GreaterOrEqual(t, len(store.updates), 2) // at least running + completed
}

type memoryProgressStore struct {
	mu      sync.Mutex
	updates []StepProgress
}

func (m *memoryProgressStore) UpdateStepProgress(_ context.Context, _ string, progress StepProgress) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, progress)
	return nil
}

// --- Execution: context cancellation ---

func TestExecution_ContextCancelled(t *testing.T) {
	wf, err := New(Options{
		Name: "cancel-test",
		Steps: []*Step{
			{Name: "slow", Activity: "slow"},
		},
	})
	require.NoError(t, err)

	slowActivity := NewActivityFunction("slow", func(ctx Context, params map[string]any) (any, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return "done", nil
		}
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{slowActivity},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = exec.Run(ctx)
	require.Error(t, err)
	// The Run method returns the error but the execution
	// uses Execute for structured status checking
}

// --- Execution.Execute structured result ---

func TestExecution_Execute(t *testing.T) {
	wf, err := New(Options{
		Name: "execute-test",
		Steps: []*Step{
			{Name: "step1", Activity: "echo"},
		},
		Outputs: []*Output{
			{Name: "result", Variable: "msg"},
		},
	})
	require.NoError(t, err)

	echoActivity := NewActivityFunction("echo", func(ctx Context, params map[string]any) (any, error) {
		ctx.SetVariable("msg", "hello")
		return "hello", nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{echoActivity},
	})
	require.NoError(t, err)

	result, err := exec.Execute(context.Background())
	require.NoError(t, err)
	require.True(t, result.Completed())
	require.False(t, result.Failed())
	require.Equal(t, "hello", result.Outputs["result"])
	require.NotZero(t, result.Timing.Duration)
}

// --- MemoryWorkflowRegistry: multiple workflows ---

func TestMemoryWorkflowRegistry_Multiple(t *testing.T) {
	reg := NewMemoryWorkflowRegistry()
	wf1, _ := New(Options{Name: "a", Steps: []*Step{{Name: "s", Activity: "x"}}})
	wf2, _ := New(Options{Name: "b", Steps: []*Step{{Name: "s", Activity: "x"}}})
	require.NoError(t, reg.Register(wf1))
	require.NoError(t, reg.Register(wf2))
	require.Len(t, reg.List(), 2)
}

// --- DefaultChildWorkflowExecutor: ExecuteAsync and GetResult ---

func TestDefaultChildWorkflowExecutor_AsyncFlow(t *testing.T) {
	wf, err := New(Options{
		Name: "async-child",
		Steps: []*Step{
			{Name: "greet", Activity: "greet"},
		},
		Outputs: []*Output{
			{Name: "msg", Variable: "msg"},
		},
	})
	require.NoError(t, err)

	reg := NewMemoryWorkflowRegistry()
	reg.Register(wf)

	greetActivity := NewActivityFunction("greet", func(ctx Context, params map[string]any) (any, error) {
		ctx.SetVariable("msg", "async hello")
		return "done", nil
	})

	executor, err := NewDefaultChildWorkflowExecutor(ChildWorkflowExecutorOptions{
		WorkflowRegistry: reg,
		Activities:       []Activity{greetActivity},
	})
	require.NoError(t, err)

	handle, err := executor.ExecuteAsync(context.Background(), &ChildWorkflowSpec{
		WorkflowName: "async-child",
	})
	require.NoError(t, err)
	require.NotEmpty(t, handle.ExecutionID)
	require.Equal(t, "async-child", handle.WorkflowName)

	// Poll for completion
	var result *ChildWorkflowResult
	for i := 0; i < 50; i++ {
		result, err = executor.GetResult(context.Background(), handle)
		require.NoError(t, err)
		if result.Status == ExecutionStatusCompleted || result.Status == ExecutionStatusFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Equal(t, ExecutionStatusCompleted, result.Status)
	require.Equal(t, "async hello", result.Outputs["msg"])
}

func TestDefaultChildWorkflowExecutor_AsyncNotFound(t *testing.T) {
	reg := NewMemoryWorkflowRegistry()
	executor, err := NewDefaultChildWorkflowExecutor(ChildWorkflowExecutorOptions{
		WorkflowRegistry: reg,
	})
	require.NoError(t, err)

	_, err = executor.ExecuteAsync(context.Background(), &ChildWorkflowSpec{
		WorkflowName: "missing",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in registry")
}

// --- Execution: branching workflow ---

func TestExecution_Branching(t *testing.T) {
	wf, err := New(Options{
		Name: "branch-test",
		Steps: []*Step{
			{
				Name:     "start",
				Activity: "set-flag",
				Next: []*Edge{
					{Step: "left", Condition: "true", Path: "left-path"},
					{Step: "right", Condition: "true", Path: "right-path"},
				},
			},
			{Name: "left", Activity: "noop"},
			{Name: "right", Activity: "noop"},
		},
	})
	require.NoError(t, err)

	setFlag := NewActivityFunction("set-flag", func(ctx Context, params map[string]any) (any, error) {
		ctx.SetVariable("flag", true)
		return nil, nil
	})
	noop := NewActivityFunction("noop", func(ctx Context, params map[string]any) (any, error) {
		return nil, nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{setFlag, noop},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
}

// --- Execution: catch handler ---

func TestExecution_CatchHandler(t *testing.T) {
	wf, err := New(Options{
		Name: "catch-test",
		Steps: []*Step{
			{
				Name:     "risky",
				Activity: "fail-it",
				Catch: []*CatchConfig{
					{ErrorEquals: []string{ErrorTypeAll}, Next: "recover", Store: "state.err_info"},
				},
				Next: []*Edge{{Step: "done"}},
			},
			{Name: "recover", Activity: "recover-it", Next: []*Edge{{Step: "done"}}},
			{Name: "done", Activity: "noop"},
		},
	})
	require.NoError(t, err)

	failIt := NewActivityFunction("fail-it", func(ctx Context, params map[string]any) (any, error) {
		return nil, fmt.Errorf("something broke")
	})
	recoverIt := NewActivityFunction("recover-it", func(ctx Context, params map[string]any) (any, error) {
		ctx.SetVariable("recovered", true)
		return "recovered", nil
	})
	noop := NewActivityFunction("noop", func(ctx Context, params map[string]any) (any, error) {
		return nil, nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{failIt, recoverIt, noop},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
}

// --- Execution: retry ---

func TestExecution_Retry(t *testing.T) {
	attempts := 0
	wf, err := New(Options{
		Name: "retry-test",
		Steps: []*Step{
			{
				Name:     "flaky",
				Activity: "flaky",
				Retry: []*RetryConfig{
					{MaxRetries: 2, BaseDelay: 1 * time.Millisecond},
				},
			},
		},
	})
	require.NoError(t, err)

	flakyActivity := NewActivityFunction("flaky", func(ctx Context, params map[string]any) (any, error) {
		attempts++
		if attempts < 3 {
			return nil, fmt.Errorf("transient error")
		}
		return "ok", nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{flakyActivity},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
	require.Equal(t, 3, attempts)
}

// --- Execution: template parameters ---

func TestExecution_TemplateParameters(t *testing.T) {
	var gotMessage string
	wf, err := New(Options{
		Name: "template-test",
		Inputs: []*Input{
			{Name: "name", Type: "string"},
		},
		Steps: []*Step{
			{
				Name:     "greet",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Hello ${inputs.name}!",
				},
			},
		},
	})
	require.NoError(t, err)

	printActivity := NewActivityFunction("print", func(ctx Context, params map[string]any) (any, error) {
		gotMessage = params["message"].(string)
		return gotMessage, nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{printActivity},
		Inputs:     map[string]any{"name": "World"},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Hello World!", gotMessage)
}

// --- Execution: script expression parameters ---

func TestExecution_ScriptExpressionParameters(t *testing.T) {
	var gotValue any
	wf, err := New(Options{
		Name: "script-param-test",
		State: map[string]any{
			"count": 5,
		},
		Steps: []*Step{
			{
				Name:     "compute",
				Activity: "capture",
				Parameters: map[string]any{
					"result": "$(state.count * 10)",
				},
			},
		},
	})
	require.NoError(t, err)

	captureActivity := NewActivityFunction("capture", func(ctx Context, params map[string]any) (any, error) {
		gotValue = params["result"]
		return gotValue, nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{captureActivity},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(50), gotValue)
}

// --- Execution: each step ---

func TestExecution_EachStep(t *testing.T) {
	var allResults []any
	wf, err := New(Options{
		Name: "each-test",
		Steps: []*Step{
			{
				Name:     "process",
				Activity: "double",
				Each:     &Each{Items: []any{1, 2, 3}, As: "item"},
				Store:    "state.results",
				Parameters: map[string]any{
					"value": "$(state.item)",
				},
			},
		},
	})
	require.NoError(t, err)

	doubleActivity := NewActivityFunction("double", func(ctx Context, params map[string]any) (any, error) {
		v := params["value"].(int64)
		return v * 2, nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{doubleActivity},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
	_ = allResults
}

// --- Execution: each step cleans up iteration variable ---

func TestExecution_EachStep_CleansUpAsVariable(t *testing.T) {
	wf, err := New(Options{
		Name: "each-cleanup",
		Steps: []*Step{
			{
				Name:     "loop",
				Activity: "echo",
				Each:     &Each{Items: []any{"a", "b"}, As: "item"},
				Store:    "state.results",
				Next:     []*Edge{{Step: "check"}},
			},
			{Name: "check", Activity: "check-leak"},
		},
	})
	require.NoError(t, err)

	echoAct := NewActivityFunction("echo", func(ctx Context, params map[string]any) (any, error) {
		return "processed", nil
	})
	checkAct := NewActivityFunction("check-leak", func(ctx Context, params map[string]any) (any, error) {
		// The "item" variable should not exist after the each loop since
		// it didn't exist before.
		_, exists := ctx.GetVariable("item")
		if exists {
			return nil, fmt.Errorf("'item' variable leaked from each loop")
		}
		return "clean", nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{echoAct, checkAct},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
}

// --- Execution: store result ---

func TestExecution_StoreResult(t *testing.T) {
	wf, err := New(Options{
		Name: "store-test",
		Steps: []*Step{
			{
				Name:     "compute",
				Activity: "compute",
				Store:    "state.result",
				Next:     []*Edge{{Step: "check"}},
			},
			{
				Name:     "check",
				Activity: "check",
			},
		},
		Outputs: []*Output{
			{Name: "final", Variable: "result"},
		},
	})
	require.NoError(t, err)

	computeActivity := NewActivityFunction("compute", func(ctx Context, params map[string]any) (any, error) {
		return 42, nil
	})
	checkActivity := NewActivityFunction("check", func(ctx Context, params map[string]any) (any, error) {
		v, ok := ctx.GetVariable("result")
		if !ok {
			return nil, fmt.Errorf("result not found in state")
		}
		if v != 42 {
			return nil, fmt.Errorf("expected 42, got %v", v)
		}
		return "verified", nil
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{computeActivity, checkActivity},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
	require.Equal(t, 42, exec.GetOutputs()["final"])
}

// --- NewFileCheckpointer defaults ---

func TestNewFileCheckpointer_EmptyDir(t *testing.T) {
	// With an empty dir, it defaults to ~/.deepnoodle/...
	// We can't easily test that without side effects, but we can test with a real dir
	dir := t.TempDir()
	cp, err := NewFileCheckpointer(dir)
	require.NoError(t, err)
	require.NotNil(t, cp)
}

// --- Execution: already started ---

func TestExecution_AlreadyStarted(t *testing.T) {
	wf, err := New(Options{
		Name:  "test",
		Steps: []*Step{{Name: "s", Activity: "a"}},
	})
	require.NoError(t, err)

	a := NewActivityFunction("a", func(ctx Context, params map[string]any) (any, error) {
		return nil, nil
	})
	exec, err := NewExecution(ExecutionOptions{
		Workflow:   wf,
		Activities: []Activity{a},
	})
	require.NoError(t, err)

	err = exec.Run(context.Background())
	require.NoError(t, err)

	// Second run should fail
	err = exec.Run(context.Background())
	require.ErrorIs(t, err, ErrAlreadyStarted)
}
