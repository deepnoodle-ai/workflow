package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/services"
)

// TaskCompletionCallback is called after a task is completed to advance the workflow.
// This allows the orchestrator to trigger workflow state transitions when remote
// workers complete tasks.
type TaskCompletionCallback func(ctx context.Context, claimed *domain.TaskClaimed, result *domain.TaskResult)

// Handler implements HTTP handlers for task and execution operations.
type Handler struct {
	taskService          *services.TaskService
	executionService     *services.ExecutionService
	onTaskCompleted      TaskCompletionCallback
}

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	TaskService      *services.TaskService
	ExecutionService *services.ExecutionService
	// OnTaskCompleted is called after a task is successfully completed.
	// The orchestrator uses this to advance workflow execution.
	OnTaskCompleted  TaskCompletionCallback
}

// NewHandler creates a new Handler.
func NewHandler(opts HandlerOptions) *Handler {
	return &Handler{
		taskService:      opts.TaskService,
		executionService: opts.ExecutionService,
		onTaskCompleted:  opts.OnTaskCompleted,
	}
}

// ClaimTask handles POST /tasks/claim.
// Worker ID is read from X-Worker-ID header.
// Returns 204 No Content if no tasks are available.
func (h *Handler) ClaimTask(w http.ResponseWriter, r *http.Request) {
	workerID := WorkerIDFromContext(r.Context())
	if workerID == "" {
		workerID = r.Header.Get("X-Worker-ID")
	}
	if workerID == "" {
		http.Error(w, "X-Worker-ID header required", http.StatusBadRequest)
		return
	}

	task, err := h.taskService.Claim(r.Context(), workerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(task)
}

// CompleteTask handles POST /tasks/{id}/complete.
// Worker ID is read from X-Worker-ID header.
func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}

	workerID := WorkerIDFromContext(r.Context())
	if workerID == "" {
		workerID = r.Header.Get("X-Worker-ID")
	}
	if workerID == "" {
		http.Error(w, "X-Worker-ID header required", http.StatusBadRequest)
		return
	}

	var result domain.TaskResult
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get task info before completing (needed for callback)
	var taskRecord *domain.TaskRecord
	if h.onTaskCompleted != nil {
		var err error
		taskRecord, err = h.taskService.Get(r.Context(), taskID)
		if err != nil {
			http.Error(w, "failed to get task: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := h.taskService.Complete(r.Context(), taskID, workerID, &result); err != nil {
		if strings.Contains(err.Error(), "not owned") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Trigger workflow advancement callback
	if h.onTaskCompleted != nil && taskRecord != nil {
		claimed := &domain.TaskClaimed{
			ID:           taskRecord.ID,
			ExecutionID:  taskRecord.ExecutionID,
			PathID:       taskRecord.PathID,
			StepName:     taskRecord.StepName,
			ActivityName: taskRecord.ActivityName,
			Attempt:      taskRecord.Attempt,
			Spec:         taskRecord.Spec,
		}
		h.onTaskCompleted(r.Context(), claimed, &result)
	}

	w.WriteHeader(http.StatusOK)
}

// HeartbeatTask handles POST /tasks/{id}/heartbeat.
// Worker ID is read from X-Worker-ID header.
func (h *Handler) HeartbeatTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}

	workerID := WorkerIDFromContext(r.Context())
	if workerID == "" {
		workerID = r.Header.Get("X-Worker-ID")
	}
	if workerID == "" {
		http.Error(w, "X-Worker-ID header required", http.StatusBadRequest)
		return
	}

	if err := h.taskService.Heartbeat(r.Context(), taskID, workerID); err != nil {
		if strings.Contains(err.Error(), "not owned") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ReleaseTaskRequest is the request body for releasing a task.
type ReleaseTaskRequest struct {
	RetryAfter time.Duration `json:"retry_after"`
}

// ReleaseTask handles POST /tasks/{id}/release.
// Worker ID is read from X-Worker-ID header.
func (h *Handler) ReleaseTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}

	workerID := WorkerIDFromContext(r.Context())
	if workerID == "" {
		workerID = r.Header.Get("X-Worker-ID")
	}
	if workerID == "" {
		http.Error(w, "X-Worker-ID header required", http.StatusBadRequest)
		return
	}

	var req ReleaseTaskRequest
	json.NewDecoder(r.Body).Decode(&req) // Ignore errors, use zero value

	if err := h.taskService.Release(r.Context(), taskID, workerID, req.RetryAfter); err != nil {
		if strings.Contains(err.Error(), "not owned") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// GetTask handles GET /tasks/{id}.
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}

	task, err := h.taskService.Get(r.Context(), taskID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(task)
}

// SubmitExecutionRequest is the request body for submitting an execution.
type SubmitExecutionRequest struct {
	WorkflowName string         `json:"workflow_name"`
	ExecutionID  string         `json:"execution_id,omitempty"`
	Inputs       map[string]any `json:"inputs,omitempty"`
}

// SubmitExecutionResponse is the response body for submitting an execution.
type SubmitExecutionResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// SubmitExecution handles POST /executions.
func (h *Handler) SubmitExecution(w http.ResponseWriter, r *http.Request) {
	if h.executionService == nil {
		http.Error(w, "execution service not configured", http.StatusServiceUnavailable)
		return
	}

	var req SubmitExecutionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.WorkflowName == "" {
		http.Error(w, "workflow_name is required", http.StatusBadRequest)
		return
	}

	// Note: The actual workflow submission would need the workflow definition
	// This is a placeholder - in practice, workflows would be registered with the engine
	http.Error(w, "workflow submission via HTTP not yet implemented", http.StatusNotImplemented)
}

// GetExecution handles GET /executions/{id}.
func (h *Handler) GetExecution(w http.ResponseWriter, r *http.Request) {
	if h.executionService == nil {
		http.Error(w, "execution service not configured", http.StatusServiceUnavailable)
		return
	}

	execID := r.PathValue("id")
	if execID == "" {
		http.Error(w, "execution id is required", http.StatusBadRequest)
		return
	}

	exec, err := h.executionService.Get(r.Context(), execID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(exec)
}

// ListExecutions handles GET /executions.
func (h *Handler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	if h.executionService == nil {
		http.Error(w, "execution service not configured", http.StatusServiceUnavailable)
		return
	}

	filter := domain.ExecutionFilter{
		WorkflowName: r.URL.Query().Get("workflow_name"),
	}

	executions, err := h.executionService.List(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(executions)
}

// CancelExecution handles POST /executions/{id}/cancel.
func (h *Handler) CancelExecution(w http.ResponseWriter, r *http.Request) {
	if h.executionService == nil {
		http.Error(w, "execution service not configured", http.StatusServiceUnavailable)
		return
	}

	execID := r.PathValue("id")
	if execID == "" {
		http.Error(w, "execution id is required", http.StatusBadRequest)
		return
	}

	if err := h.executionService.Cancel(r.Context(), execID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
