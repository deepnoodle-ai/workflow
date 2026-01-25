package workflow

import "github.com/deepnoodle-ai/workflow/domain"

// PathState tracks the state of an execution path.
type PathState = domain.PathState

// JoinState tracks a path waiting at a join step.
type JoinState = domain.JoinState
