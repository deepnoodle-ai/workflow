package hosttest

import (
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

// The durable names, unqualified, as a backend's own tests read them.
type (
	NewRun     = durable.NewRun
	Result     = durable.Result
	Finished   = durable.Finished
	SQLTx      = durable.SQLTx
	StepStatus = durable.StepStatus
	StepRecord = durable.StepRecord
	JobRef     = durable.JobRef
	Execution  = durable.Execution
	Observer   = durable.Observer
	Settlement = durable.Settlement
	Outcome    = durable.Outcome
	QueueStat  = durable.QueueStat

	claimedJob = hostcore.Claimed
)

const (
	StepDispatched = durable.StepDispatched
	StepCompleted  = durable.StepCompleted
	StepFailed     = durable.StepFailed
	StepCancelled  = durable.StepCancelled

	OutcomeCompleted = durable.OutcomeCompleted
	OutcomeFailed    = durable.OutcomeFailed
	OutcomeDeferred  = durable.OutcomeDeferred
	OutcomeFired     = durable.OutcomeFired

	EventAborted                   = durable.EventAborted
	ErrorTypeDefinitionUnavailable = durable.ErrorTypeDefinitionUnavailable
)

var (
	ErrRunNotFound           = durable.ErrRunNotFound
	ErrRunExists             = durable.ErrRunExists
	ErrUnstorable            = durable.ErrUnstorable
	ErrDefinitionUnavailable = durable.ErrDefinitionUnavailable
	ErrNotClaimable          = durable.ErrNotClaimable
	ErrHardStop              = durable.ErrHardStop

	errLeaseLost    = hostcore.ErrLeaseLost
	errShuttingDown = hostcore.ErrShuttingDown
)
