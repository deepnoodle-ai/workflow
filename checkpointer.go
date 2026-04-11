package workflow

import (
	"context"
)

// Checkpointer is the small interface the engine uses to persist and
// load execution snapshots. Consumers plug in their own storage
// (Postgres, Redis, S3, etc.) by implementing these three methods.
//
// The built-in FileCheckpointer and MemoryCheckpointer exist for
// development and testing only; production deployments should
// provide their own implementation.
type Checkpointer interface {
	// SaveCheckpoint persists the given checkpoint snapshot. The
	// engine sets SchemaVersion = CheckpointSchemaVersion on every
	// call; implementations should record it verbatim.
	SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error

	// LoadCheckpoint returns the most recent checkpoint for the
	// given execution ID. Returns ErrNoCheckpoint when no
	// checkpoint exists for the execution. Implementations MUST
	// reject any loaded checkpoint whose SchemaVersion is greater
	// than CheckpointSchemaVersion — the library did not write it
	// and cannot safely interpret it.
	LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error)

	// DeleteCheckpoint removes checkpoint data for an execution.
	DeleteCheckpoint(ctx context.Context, executionID string) error
}

// AtomicCheckpointer is an optional side interface a Checkpointer
// may implement to expose a compare-and-swap style update path. When
// the engine needs to mutate a checkpoint in-place (e.g.
// PauseBranchInCheckpoint), it prefers AtomicUpdate over a naive
// load-modify-write sequence.
//
// A Checkpointer that does not implement AtomicCheckpointer still
// works — the engine falls back to load-modify-write and accepts the
// race window that implies. Backends with real transactional
// primitives (Postgres SELECT ... FOR UPDATE, Redis WATCH/MULTI,
// etcd CAS) should implement this interface to close that window.
type AtomicCheckpointer interface {
	// AtomicUpdate loads the checkpoint for the given execution,
	// runs fn against the loaded copy, and saves the result. The
	// entire read-modify-write cycle must be atomic with respect to
	// other writers of the same execution.
	//
	// If fn returns an error the checkpoint MUST NOT be saved; the
	// error is returned verbatim to the caller.
	//
	// If no checkpoint exists for the execution ID, AtomicUpdate
	// returns ErrNoCheckpoint without invoking fn.
	AtomicUpdate(ctx context.Context, executionID string, fn func(*Checkpoint) error) error
}
