package workflow

import "time"

// CheckpointSchemaVersion is the current wire-format version for
// checkpoints written by this library. It is embedded in every saved
// Checkpoint so readers can detect and reject incompatible shapes.
//
// Version contract:
//
//   - v1: Initial stable schema. Locked at v1 release.
//
// Consumers that persist checkpoints to their own storage MUST treat
// SchemaVersion as a forward-compatibility signal: if a loaded
// checkpoint has a SchemaVersion higher than the library version
// understands, the load should fail rather than proceed with a
// potentially wrong interpretation.
const CheckpointSchemaVersion = 1

// Checkpoint is the serialized snapshot of an execution. It is the
// single unit of persistence the engine writes and reads when
// checkpointing, resuming, and inspecting running or dormant
// executions.
//
// # Round-trip contract
//
//   - The engine marshals Checkpoint to JSON via encoding/json.
//   - Consumers may swap a different encoder for storage, but MUST
//     produce a byte stream that round-trips back through the same
//     struct with every field preserved.
//   - SchemaVersion is set on every save by the engine. Readers must
//     reject any checkpoint whose SchemaVersion is higher than the
//     library's CheckpointSchemaVersion.
//
// The JSON tag on every field is part of the stable format. Adding a
// field is always safe (zero value on load from an older writer);
// renaming or removing a field is a schema break.
//
// # Load-bearing fields
//
// The orchestrator's resume logic depends on these fields surviving
// the round-trip; a custom encoder that drops them will silently
// corrupt resumed executions:
//
//   - BranchState.Variables — branch-local state that activities
//     read and write. Without it, resumed branches restart from a
//     blank state.
//   - BranchState.Wait — the active wait/sleep state. Without it,
//     a branch that was hard-suspended on a signal-wait or sleep
//     can't be re-parked on resume; the engine treats it as a fresh
//     advance.
//   - BranchState.PauseRequested — whether the branch was paused
//     by an operator or a Pause step. Without it, a paused branch
//     resumes as if it had never been paused.
//   - BranchState.ActivityHistory / ActivityHistoryStep — the
//     per-step replay cache used by Context.History. Without it,
//     activities re-execute side effects on every wait-unwind
//     replay.
//
// All other fields are advisory or recoverable from the workflow
// definition.
type Checkpoint struct {
	// SchemaVersion is the wire-format version. Set to
	// CheckpointSchemaVersion by the engine on every save.
	SchemaVersion int `json:"schema_version"`

	// ID is the unique identifier for this checkpoint snapshot.
	// Distinct from ExecutionID: a single execution writes many
	// checkpoints over its lifetime.
	ID string `json:"id"`

	// ExecutionID is the ID of the execution this checkpoint belongs
	// to. Stable across all snapshots of the same execution.
	ExecutionID string `json:"execution_id"`

	// WorkflowName is the name of the workflow definition used to
	// produce the execution. Informational — the engine does not
	// consult it on resume.
	WorkflowName string `json:"workflow_name"`

	// Status is the execution status at the time the checkpoint was
	// written.
	Status ExecutionStatus `json:"status"`

	// Inputs is the workflow's input values, as supplied on
	// NewExecution. Immutable for the lifetime of the execution.
	Inputs map[string]any `json:"inputs"`

	// Outputs is the declared workflow outputs extracted from the
	// final branch states when the execution completes. Empty until
	// then.
	Outputs map[string]any `json:"outputs"`

	// Variables is the top-level workflow state, distinct from
	// per-branch state. Used by the orchestrator for shared values
	// that are not branch-local.
	Variables map[string]any `json:"variables"`

	// BranchStates holds the per-branch persisted state, keyed by
	// branch ID. Each entry captures the branch's variables, current
	// step, retry counters, and any active WaitState.
	BranchStates map[string]*BranchState `json:"branch_states"`

	// JoinStates holds the persisted state for active join steps,
	// keyed by join step name.
	JoinStates map[string]*JoinState `json:"join_states"`

	// BranchCounter is the monotonic counter used to allocate
	// branch IDs. Persisted so resumed executions continue to
	// allocate unique IDs.
	BranchCounter int `json:"branch_counter"`

	// Error is the terminal error message when Status is
	// ExecutionStatusFailed. Empty otherwise.
	Error string `json:"error,omitempty"`

	// StartTime is when the execution first began running.
	StartTime time.Time `json:"start_time,omitzero"`

	// EndTime is when the execution reached a terminal status
	// (completed, failed, or canceled). Zero for in-flight or
	// suspended executions.
	EndTime time.Time `json:"end_time,omitzero"`

	// CheckpointAt is when this snapshot was written.
	CheckpointAt time.Time `json:"checkpoint_at"`
}
