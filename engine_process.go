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

			e.processExecution(ctx, lease)
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
