package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// processLoop continuously dequeues and processes work items.
func (e *Engine) processLoop(ctx context.Context) {
	var sem chan struct{}
	if e.maxConcurrent > 0 {
		sem = make(chan struct{}, e.maxConcurrent)
	}

	for {
		if e.stopping.Load() {
			return
		}

		// Acquire semaphore FIRST, then dequeue
		// This ensures we have capacity before claiming a lease,
		// preventing lease expiry while waiting for capacity
		if sem != nil {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}

		// Dequeue with lease
		lease, err := e.queue.Dequeue(ctx)
		if err != nil {
			if sem != nil {
				<-sem // release slot on error
			}
			if ctx.Err() != nil {
				return
			}
			e.logger.Warn("dequeue error", "error", err)
			continue
		}

		e.activeWg.Add(1)
		go func(lease Lease) {
			defer e.activeWg.Done()
			defer func() {
				if sem != nil {
					<-sem
				}
			}()

			// Use execution context (not loop context) for actual execution
			e.processExecution(e.execCtx, lease)
		}(lease)
	}
}

// processExecution handles a single execution from the queue.
func (e *Engine) processExecution(ctx context.Context, lease Lease) {
	id := lease.Item.ExecutionID

	record, err := e.store.Get(ctx, id)
	if err != nil {
		e.logger.Error("failed to load execution", "id", id, "error", err)
		if nackErr := e.queue.Nack(ctx, lease.Token, time.Minute); nackErr != nil {
			e.logger.Error("failed to nack", "id", id, "error", nackErr)
		}
		return
	}

	// Handle based on environment mode
	switch env := e.env.(type) {
	case BlockingEnvironment:
		e.processBlocking(ctx, lease, record, env)
	case DispatchEnvironment:
		e.processDispatch(ctx, lease, record, env)
	default:
		e.logger.Error("unknown environment type", "id", id)
		if nackErr := e.queue.Nack(ctx, lease.Token, time.Minute); nackErr != nil {
			e.logger.Error("failed to nack", "id", id, "error", nackErr)
		}
	}
}

// processBlocking handles execution in a blocking environment (local).
func (e *Engine) processBlocking(ctx context.Context, lease Lease, record *ExecutionRecord, env BlockingEnvironment) {
	id := record.ID
	attempt := record.Attempt

	// Claim with fencing (status must be pending, attempt must match)
	claimed, err := e.store.ClaimExecution(ctx, id, e.workerID, attempt)
	if err != nil {
		e.logger.Error("failed to claim execution", "id", id, "error", err)
		if nackErr := e.queue.Nack(ctx, lease.Token, time.Minute); nackErr != nil {
			e.logger.Error("failed to nack", "id", id, "error", nackErr)
		}
		return
	}
	if !claimed {
		// Either not pending or attempt changed - ack and move on
		e.logger.Debug("execution not claimable", "id", id, "attempt", attempt)
		if ackErr := e.queue.Ack(ctx, lease.Token); ackErr != nil {
			e.logger.Error("failed to ack", "id", id, "error", ackErr)
		}
		return
	}

	// Invoke OnExecutionStarted callback
	e.callbacks.OnExecutionStarted(id)

	// Start heartbeat goroutine
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go e.heartbeatLoop(heartbeatCtx, id, lease.Token)

	// Load or create execution
	exec, err := e.loadExecution(ctx, record)
	if err != nil {
		e.completeExecution(ctx, lease, record, attempt, EngineStatusFailed, nil, err.Error())
		return
	}

	// Run workflow
	if err := env.Run(ctx, exec); err != nil {
		e.completeExecution(ctx, lease, record, attempt, EngineStatusFailed, nil, err.Error())
		return
	}

	// Complete successfully
	e.completeExecution(ctx, lease, record, attempt, EngineStatusCompleted, exec.GetOutputs(), "")
}

// processDispatch handles execution in a dispatch environment (remote).
func (e *Engine) processDispatch(ctx context.Context, lease Lease, record *ExecutionRecord, env DispatchEnvironment) {
	id := record.ID
	attempt := record.Attempt

	// Mark dispatched for stale detection (worker must claim within grace period)
	if err := e.store.MarkDispatched(ctx, id, attempt); err != nil {
		e.logger.Error("failed to mark dispatched", "id", id, "error", err)
		if nackErr := e.queue.Nack(ctx, lease.Token, time.Minute); nackErr != nil {
			e.logger.Error("failed to nack", "id", id, "error", nackErr)
		}
		return
	}

	// Dispatch to remote worker
	if err := env.Dispatch(ctx, id, attempt); err != nil {
		e.logger.Error("failed to dispatch execution", "id", id, "error", err)
		if nackErr := e.queue.Nack(ctx, lease.Token, time.Minute); nackErr != nil {
			e.logger.Error("failed to nack", "id", id, "error", nackErr)
		}
		return
	}

	// Dispatch succeeded - ack the queue item
	// The execution record tracks state. If the worker fails to claim,
	// the reaper will detect stale dispatched_at and re-enqueue.
	if ackErr := e.queue.Ack(ctx, lease.Token); ackErr != nil {
		e.logger.Error("failed to ack", "id", id, "error", ackErr)
	}
}

// loadExecution creates an Execution from an ExecutionRecord.
func (e *Engine) loadExecution(_ context.Context, record *ExecutionRecord) (*Execution, error) {
	// Look up the workflow
	e.workflowsMu.RLock()
	wf, ok := e.workflows[record.WorkflowName]
	e.workflowsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("workflow %q not found", record.WorkflowName)
	}

	// Create execution options
	execOpts := ExecutionOptions{
		Workflow:     wf,
		Inputs:       record.Inputs,
		ExecutionID:  record.ID,
		Checkpointer: e.checkpointer,
		Activities:   e.activities,
		Logger:       e.logger.With("execution_id", record.ID),
	}

	exec, err := NewExecution(execOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create execution: %w", err)
	}

	return exec, nil
}

// completeExecution persists the final status and acknowledges the queue item.
func (e *Engine) completeExecution(ctx context.Context, lease Lease, record *ExecutionRecord, attempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) {
	// Persist completion with fencing - MUST succeed before Ack
	completed, err := e.store.CompleteExecution(ctx, record.ID, attempt, status, outputs, lastError)
	if err != nil {
		e.logger.Error("failed to persist completion", "id", record.ID, "error", err)
		// Nack for retry - DO NOT ack without persistence
		if nackErr := e.queue.Nack(ctx, lease.Token, time.Minute); nackErr != nil {
			e.logger.Error("failed to nack", "id", record.ID, "error", nackErr)
		}
		return
	}
	if !completed {
		// Attempt changed - we're stale, just ack and move on
		e.logger.Warn("completion fenced out", "id", record.ID, "attempt", attempt)
		if ackErr := e.queue.Ack(ctx, lease.Token); ackErr != nil {
			e.logger.Error("failed to ack", "id", record.ID, "error", ackErr)
		}
		return
	}

	// Persistence succeeded - now safe to ack
	if ackErr := e.queue.Ack(ctx, lease.Token); ackErr != nil {
		e.logger.Error("failed to ack", "id", record.ID, "error", ackErr)
	}

	// Invoke callback
	var execErr error
	if lastError != "" {
		execErr = errors.New(lastError)
	}
	duration := time.Since(record.StartedAt)
	e.callbacks.OnExecutionCompleted(record.ID, duration, execErr)
}

// heartbeatLoop sends periodic heartbeats and extends the queue lease.
func (e *Engine) heartbeatLoop(ctx context.Context, id, leaseToken string) {
	ticker := time.NewTicker(e.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.store.Heartbeat(ctx, id, e.workerID); err != nil {
				e.logger.Warn("heartbeat failed", "id", id, "error", err)
			}
			// Also extend the queue lease
			if err := e.queue.Extend(ctx, leaseToken, 5*time.Minute); err != nil {
				e.logger.Warn("lease extend failed", "id", id, "error", err)
			}
		}
	}
}

// recoverOrphaned recovers executions that were pending or running when the engine
// previously stopped (crash, restart, etc). Based on recovery mode:
// - RecoveryResume: Re-enqueue with incremented attempt for retry
// - RecoveryFail: Mark as failed
func (e *Engine) recoverOrphaned(ctx context.Context) error {
	// Find all pending and running executions
	orphaned, err := e.store.List(ctx, ListFilter{
		Statuses: []EngineExecutionStatus{
			EngineStatusPending,
			EngineStatusRunning,
		},
	})
	if err != nil {
		return err
	}

	if len(orphaned) == 0 {
		return nil
	}

	e.logger.Info("recovering orphaned executions",
		"count", len(orphaned),
		"mode", e.recoveryMode)

	for _, record := range orphaned {
		if err := e.recoverExecution(ctx, record, "startup recovery"); err != nil {
			e.logger.Warn("failed to recover execution",
				"id", record.ID,
				"error", err)
			// Continue with other executions
		}
	}

	return nil
}

// recoverExecution handles recovery of a single execution based on recovery mode.
func (e *Engine) recoverExecution(ctx context.Context, record *ExecutionRecord, reason string) error {
	switch e.recoveryMode {
	case RecoveryResume:
		return e.resumeExecution(ctx, record, reason)
	case RecoveryFail:
		return e.failExecution(ctx, record, reason)
	default:
		return fmt.Errorf("unknown recovery mode: %s", e.recoveryMode)
	}
}

// resumeExecution increments the attempt, resets status to pending, and re-enqueues.
func (e *Engine) resumeExecution(ctx context.Context, record *ExecutionRecord, reason string) error {
	e.logger.Info("resuming orphaned execution",
		"id", record.ID,
		"previous_status", record.Status,
		"previous_attempt", record.Attempt,
		"reason", reason)

	// Increment attempt for fencing
	record.Attempt++
	record.Status = EngineStatusPending
	record.WorkerID = ""
	record.LastHeartbeat = time.Time{}
	record.DispatchedAt = time.Time{}

	if err := e.store.Update(ctx, record); err != nil {
		return fmt.Errorf("failed to update record: %w", err)
	}

	if err := e.queue.Enqueue(ctx, WorkItem{ExecutionID: record.ID}); err != nil {
		return fmt.Errorf("failed to enqueue: %w", err)
	}

	return nil
}

// failExecution marks the execution as failed.
func (e *Engine) failExecution(ctx context.Context, record *ExecutionRecord, reason string) error {
	e.logger.Info("marking orphaned execution as failed",
		"id", record.ID,
		"previous_status", record.Status,
		"reason", reason)

	record.Status = EngineStatusFailed
	record.LastError = fmt.Sprintf("execution orphaned: %s", reason)
	record.CompletedAt = time.Now()

	if err := e.store.Update(ctx, record); err != nil {
		return fmt.Errorf("failed to update record: %w", err)
	}

	return nil
}

// reaperLoop periodically checks for stale executions and recovers them.
func (e *Engine) reaperLoop(ctx context.Context) {
	ticker := time.NewTicker(e.reaperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.stopping.Load() {
				return
			}
			e.reapStaleExecutions(ctx)
		}
	}
}

// reapStaleExecutions finds and recovers stale running and pending executions.
func (e *Engine) reapStaleExecutions(ctx context.Context) {
	// Reap stale running executions (missed heartbeats)
	heartbeatCutoff := time.Now().Add(-e.heartbeatTimeout)
	staleRunning, err := e.store.ListStaleRunning(ctx, heartbeatCutoff)
	if err != nil {
		e.logger.Warn("failed to list stale running executions", "error", err)
	} else {
		for _, record := range staleRunning {
			if err := e.recoverExecution(ctx, record, "missed heartbeat"); err != nil {
				e.logger.Warn("failed to reap stale running execution",
					"id", record.ID,
					"error", err)
			}
		}
	}

	// Reap stale pending executions (dispatched but never claimed)
	dispatchCutoff := time.Now().Add(-e.dispatchTimeout)
	stalePending, err := e.store.ListStalePending(ctx, dispatchCutoff)
	if err != nil {
		e.logger.Warn("failed to list stale pending executions", "error", err)
	} else {
		for _, record := range stalePending {
			if err := e.recoverExecution(ctx, record, "dispatch not claimed"); err != nil {
				e.logger.Warn("failed to reap stale pending execution",
					"id", record.ID,
					"error", err)
			}
		}
	}
}
