package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrBranchNotFound is returned by PauseBranch / UnpauseBranch when the given
// branch ID is not present in the execution's branch states.
var ErrBranchNotFound = errors.New("workflow: branch not found")

// PauseConfig configures a declarative pause step. When a branch reaches a
// step with a non-nil Pause config the branch parks with
// ExecutionStatusPaused; the step graph advances past the pause step
// (using its Next edges), so on unpause + resume the branch continues at
// the successor step without re-triggering the pause.
//
// Pause is a manual hold-point: unlike a WaitSignal or a durable Sleep
// there is no declared resumption condition — an external caller
// (operator, parent workflow, automated check) must call UnpauseBranch to
// release the branch. Use it for approval gates, production-deploy holds,
// or any point where human judgment is required before continuing.
type PauseConfig struct {
	// Reason is an optional human-readable note describing why the
	// step pauses. Stored on BranchState.PauseReason when the pause
	// triggers. The engine does not interpret it.
	Reason string `json:"reason,omitempty"`
}

// pauseRequest is emitted by Path.Run when a branch is parking due to a
// pause request — either an external PauseBranch call or a declarative
// Pause step. The orchestrator translates it into a Paused BranchState and
// hard-suspends the branch (goroutine exits, branch removed from
// activeBranches, checkpoint saved).
type pauseRequest struct {
	// StepName is the step the branch should resume at on unpause. For
	// an external PauseBranch, this is the current step (the one about
	// to run). For a declarative Pause step this is the successor
	// step — the pause step itself is considered consumed.
	StepName string
	// Reason is an optional human-readable note, copied from the
	// trigger (PauseBranch argument or PauseConfig.Reason).
	Reason string
}

// PauseBranch requests that the named branch pause at its next step
// boundary. If the branch is currently running it observes the request
// at the top of its next loop iteration and exits cleanly; if the branch
// has already exited (e.g., it's suspended on a wait), the persistent
// PauseRequested flag ensures the pause takes effect the next time the
// branch is reconstructed from checkpoint.
//
// PauseBranch is idempotent: pausing an already-paused branch is a no-op.
// The reason, if provided, overwrites any previously-recorded reason.
//
// PauseBranch returns ErrBranchNotFound if no branch with the given ID
// exists in this execution's state.
func (e *Execution) PauseBranch(branchID, reason string) error {
	return e.pauseBranchLocked(branchID, reason)
}

// UnpauseBranch clears the pause request on the named branch. If the
// execution loop is still running and the branch is still in activeBranches
// (i.e., PauseBranch was called but the branch hasn't yet hit a step
// boundary), the branch will continue normally. If the branch has already
// parked on Paused status, clearing the flag is not enough to restart
// it — the caller must invoke Run/Resume/ExecuteOrResume to spawn a
// fresh goroutine for the branch.
//
// UnpauseBranch is idempotent: unpausing a branch that is not paused is a
// no-op. Returns ErrBranchNotFound if no such branch exists.
func (e *Execution) UnpauseBranch(branchID string) error {
	return e.unpauseBranchLocked(branchID)
}

func (e *Execution) pauseBranchLocked(branchID, reason string) error {
	var found bool
	e.state.UpdateBranchState(branchID, func(state *BranchState) {
		state.PauseRequested = true
		state.PauseReason = reason
		freezeWaitOnPause(state, time.Now())
		found = true
	})
	if !found {
		return fmt.Errorf("%w: %q", ErrBranchNotFound, branchID)
	}
	if active, ok := e.getActiveBranch(branchID); ok {
		active.requestPause(reason)
	}
	return nil
}

func (e *Execution) unpauseBranchLocked(branchID string) error {
	var found bool
	e.state.UpdateBranchState(branchID, func(state *BranchState) {
		state.PauseRequested = false
		state.PauseReason = ""
		thawWaitOnUnpause(state, time.Now())
		found = true
	})
	if !found {
		return fmt.Errorf("%w: %q", ErrBranchNotFound, branchID)
	}
	if active, ok := e.getActiveBranch(branchID); ok {
		active.clearPause()
	}
	return nil
}

// freezeWaitOnPause captures the time remaining on the branch's pending
// wait (signal-wait or sleep) and clears the absolute WakeAt so the
// pause duration does not count against the wait clock. No-op when
// there is no pending wait or the wait has no deadline (zero WakeAt).
// Idempotent: a double-pause does not re-compute a smaller remaining.
//
// Both signal waits and sleeps participate. The principle is the same
// for either: an operator-driven pause must not consume the wait's
// timeout budget. Pre-2026-04 the engine froze sleeps only and let
// signal-wait deadlines tick during pause; that asymmetry was a
// latent SLA bug because pausing a branch waiting on a long-deadline
// callback could silently exhaust the timeout.
func freezeWaitOnPause(state *BranchState, now time.Time) {
	if state.Wait == nil {
		return
	}
	if state.Wait.WakeAt.IsZero() {
		// No deadline (or already frozen).
		return
	}
	remaining := state.Wait.WakeAt.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	state.Wait.Remaining = remaining
	state.Wait.WakeAt = time.Time{}
}

// thawWaitOnUnpause rebases a frozen wait's absolute WakeAt to
// now + Remaining and clears the Remaining marker. No-op when there
// is no pending wait or the wait isn't frozen.
//
// A frozen wait is identified by WakeAt.IsZero() — freezeWaitOnPause
// always clears WakeAt when it captures Remaining. Remaining may be
// zero if the wait had already expired at freeze time; in that case
// the rebase to now + 0 == now causes the handler on resume to wake
// (or time out) immediately, which is the correct behavior (the wait
// owed zero remaining duration when it was paused). Without this
// handling the frozen zero/zero wait would be stuck forever.
func thawWaitOnUnpause(state *BranchState, now time.Time) {
	if state.Wait == nil {
		return
	}
	if !state.Wait.WakeAt.IsZero() {
		// Not frozen (or already thawed): leave the absolute
		// deadline in place.
		return
	}
	state.Wait.WakeAt = now.Add(state.Wait.Remaining)
	state.Wait.Remaining = 0
}

// PauseBranchInCheckpoint loads the latest checkpoint for the given
// execution, flips the target branch's PauseRequested flag, and saves the
// checkpoint back. It is the operator-facing entry point for pausing an
// execution that is not currently loaded in any host process.
//
// If the branch is parked on a durable wait (signal-wait or sleep), the
// wait's absolute WakeAt is captured into Remaining and cleared, so the
// pause duration does not consume the wait's timeout budget. See
// freezeWaitOnPause.
//
// The operation is a non-atomic load-modify-write against the
// Checkpointer. If a host process is concurrently running the same
// execution, the save here may race with the host's own checkpoint
// writes. Consumers requiring strict atomicity should use a
// Checkpointer implementation that serializes writes (e.g. a Postgres
// backend with row-level locking or optimistic concurrency on a
// version column).
//
// PauseBranchInCheckpoint is idempotent: pausing an already-paused branch
// is a no-op save. Returns ErrNoCheckpoint if no checkpoint exists for
// the execution ID, or ErrBranchNotFound if the branch is not in the
// checkpoint.
func PauseBranchInCheckpoint(ctx context.Context, cp Checkpointer, executionID, branchID, reason string) error {
	return mutatePauseInCheckpoint(ctx, cp, executionID, branchID, true, reason)
}

// UnpauseBranchInCheckpoint is the operator-facing entry point for
// clearing a branch's pause flag in a checkpoint without loading the
// execution. If the branch's wait was frozen by a prior pause, its
// WakeAt is rebased to now + Remaining so the wait resumes with the
// time it had left at pause time. See PauseBranchInCheckpoint for the
// concurrency contract.
func UnpauseBranchInCheckpoint(ctx context.Context, cp Checkpointer, executionID, branchID string) error {
	return mutatePauseInCheckpoint(ctx, cp, executionID, branchID, false, "")
}

func mutatePauseInCheckpoint(ctx context.Context, cp Checkpointer, executionID, branchID string, paused bool, reason string) error {
	if cp == nil {
		return fmt.Errorf("checkpointer is required")
	}
	apply := func(checkpoint *Checkpoint) error {
		ps, ok := checkpoint.BranchStates[branchID]
		if !ok {
			return fmt.Errorf("%w: %q (execution %q)", ErrBranchNotFound, branchID, executionID)
		}
		ps.PauseRequested = paused
		ps.PauseReason = reason
		now := time.Now()
		if paused {
			freezeWaitOnPause(ps, now)
		} else {
			thawWaitOnUnpause(ps, now)
		}
		return nil
	}

	// Prefer AtomicUpdate when the Checkpointer supports it. Backends
	// with real transactional primitives close the load-modify-write
	// race window this function would otherwise open.
	if ac, ok := cp.(AtomicCheckpointer); ok {
		return ac.AtomicUpdate(ctx, executionID, apply)
	}

	checkpoint, err := cp.LoadCheckpoint(ctx, executionID)
	if err != nil {
		return fmt.Errorf("loading checkpoint: %w", err)
	}
	if checkpoint == nil {
		return fmt.Errorf("%w: execution %q", ErrNoCheckpoint, executionID)
	}
	if err := apply(checkpoint); err != nil {
		return err
	}
	return cp.SaveCheckpoint(ctx, checkpoint)
}
