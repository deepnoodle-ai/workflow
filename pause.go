package workflow

import (
	"context"
	"errors"
	"fmt"
)

// ErrPathNotFound is returned by PausePath / UnpausePath when the given
// path ID is not present in the execution's path states.
var ErrPathNotFound = errors.New("workflow: path not found")

// PauseConfig configures a declarative pause step. When a path reaches a
// step with a non-nil Pause config the path parks with
// ExecutionStatusPaused; the step graph advances past the pause step
// (using its Next edges), so on unpause + resume the path continues at
// the successor step without re-triggering the pause.
//
// Pause is a manual hold-point: unlike a WaitSignal or a durable Sleep
// there is no declared resumption condition — an external caller
// (operator, parent workflow, automated check) must call UnpausePath to
// release the path. Use it for approval gates, production-deploy holds,
// or any point where human judgment is required before continuing.
type PauseConfig struct {
	// Reason is an optional human-readable note describing why the
	// step pauses. Stored on PathState.PauseReason when the pause
	// triggers. The engine does not interpret it.
	Reason string `json:"reason,omitempty"`
}

// PauseRequest is emitted by Path.Run when a path is parking due to a
// pause request — either an external PausePath call or a declarative
// Pause step. The orchestrator translates it into a Paused PathState and
// hard-suspends the path (goroutine exits, path removed from
// activePaths, checkpoint saved).
type PauseRequest struct {
	// StepName is the step the path should resume at on unpause. For
	// an external PausePath, this is the current step (the one about
	// to run). For a declarative Pause step this is the successor
	// step — the pause step itself is considered consumed.
	StepName string
	// Reason is an optional human-readable note, copied from the
	// trigger (PausePath argument or PauseConfig.Reason).
	Reason string
}

// PausePath requests that the named path pause at its next step
// boundary. If the path is currently running it observes the request
// at the top of its next loop iteration and exits cleanly; if the path
// has already exited (e.g., it's suspended on a wait), the persistent
// PauseRequested flag ensures the pause takes effect the next time the
// path is reconstructed from checkpoint.
//
// PausePath is idempotent: pausing an already-paused path is a no-op.
// The reason, if provided, overwrites any previously-recorded reason.
//
// PausePath returns ErrPathNotFound if no path with the given ID
// exists in this execution's state.
func (e *Execution) PausePath(pathID, reason string) error {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return e.pausePathLocked(pathID, reason)
}

// UnpausePath clears the pause request on the named path. If the
// execution loop is still running and the path is still in activePaths
// (i.e., PausePath was called but the path hasn't yet hit a step
// boundary), the path will continue normally. If the path has already
// parked on Paused status, clearing the flag is not enough to restart
// it — the caller must invoke Run/Resume/ExecuteOrResume to spawn a
// fresh goroutine for the path.
//
// UnpausePath is idempotent: unpausing a path that is not paused is a
// no-op. Returns ErrPathNotFound if no such path exists.
func (e *Execution) UnpausePath(pathID string) error {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return e.unpausePathLocked(pathID)
}

func (e *Execution) pausePathLocked(pathID, reason string) error {
	var found bool
	e.state.UpdatePathState(pathID, func(state *PathState) {
		state.PauseRequested = true
		state.PauseReason = reason
		found = true
	})
	if !found {
		return fmt.Errorf("%w: %q", ErrPathNotFound, pathID)
	}
	if active, ok := e.activePaths[pathID]; ok {
		active.requestPause(reason)
	}
	return nil
}

func (e *Execution) unpausePathLocked(pathID string) error {
	var found bool
	e.state.UpdatePathState(pathID, func(state *PathState) {
		state.PauseRequested = false
		state.PauseReason = ""
		found = true
	})
	if !found {
		return fmt.Errorf("%w: %q", ErrPathNotFound, pathID)
	}
	if active, ok := e.activePaths[pathID]; ok {
		active.clearPause()
	}
	return nil
}

// PausePathInCheckpoint loads the latest checkpoint for the given
// execution, flips the target path's PauseRequested flag, and saves the
// checkpoint back. It is the operator-facing entry point for pausing an
// execution that is not currently loaded in any host process.
//
// The operation is a non-atomic load-modify-write against the
// Checkpointer. If a host process is concurrently running the same
// execution, the save here may race with the host's own checkpoint
// writes. Consumers requiring strict atomicity should use a
// Checkpointer implementation that serializes writes (e.g. a Postgres
// backend with row-level locking or optimistic concurrency on a
// version column).
//
// PausePathInCheckpoint is idempotent: pausing an already-paused path
// is a no-op save. Returns ErrNoCheckpoint if no checkpoint exists for
// the execution ID, or ErrPathNotFound if the path is not in the
// checkpoint.
func PausePathInCheckpoint(ctx context.Context, cp Checkpointer, executionID, pathID, reason string) error {
	return mutatePauseInCheckpoint(ctx, cp, executionID, pathID, true, reason)
}

// UnpausePathInCheckpoint is the operator-facing entry point for
// clearing a path's pause flag in a checkpoint without loading the
// execution. See PausePathInCheckpoint for the concurrency contract.
func UnpausePathInCheckpoint(ctx context.Context, cp Checkpointer, executionID, pathID string) error {
	return mutatePauseInCheckpoint(ctx, cp, executionID, pathID, false, "")
}

func mutatePauseInCheckpoint(ctx context.Context, cp Checkpointer, executionID, pathID string, paused bool, reason string) error {
	checkpoint, err := cp.LoadCheckpoint(ctx, executionID)
	if err != nil {
		return fmt.Errorf("loading checkpoint: %w", err)
	}
	if checkpoint == nil {
		return fmt.Errorf("%w: execution %q", ErrNoCheckpoint, executionID)
	}
	ps, ok := checkpoint.PathStates[pathID]
	if !ok {
		return fmt.Errorf("%w: %q (execution %q)", ErrPathNotFound, pathID, executionID)
	}
	ps.PauseRequested = paused
	ps.PauseReason = reason
	return cp.SaveCheckpoint(ctx, checkpoint)
}
