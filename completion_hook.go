package workflow

import "context"

// CompletionHook is called after a workflow completes successfully. It returns
// follow-up specs describing workflows that should be triggered as a result
// of this execution.
//
// The hook runs synchronously after the execution completes. Keep it fast —
// it should build descriptors, not execute workflows. The consumer persists
// the FollowUpSpecs to their own durable outbox for async processing.
//
// Returning an error does not change the execution result — the workflow is
// still completed. The error is logged and the consumer can inspect
// result.FollowUps to see what was produced before the error.
type CompletionHook func(ctx context.Context, result *ExecutionResult) ([]FollowUpSpec, error)
