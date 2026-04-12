package workflow

import (
	"time"
)

// EdgeMatchingStrategy defines how edges should be evaluated
type EdgeMatchingStrategy string

const (
	// EdgeMatchingAll evaluates all edges and follows all matches (default behavior)
	EdgeMatchingAll EdgeMatchingStrategy = "all"

	// EdgeMatchingFirst evaluates edges in order and follows only the first matching one
	EdgeMatchingFirst EdgeMatchingStrategy = "first"
)

// Edge is used to configure a next step in a workflow.
type Edge struct {
	Step      string `json:"step"`
	Condition string `json:"condition,omitempty"`
	// BranchName optionally names the branch created when this edge
	// is followed. Empty means "continue on the current branch".
	BranchName string `json:"branch,omitempty"`
}

// Each is used to configure a step to loop over a list of items.
type Each struct {
	Items any    `json:"items"`
	As    string `json:"as,omitempty"`
}

// WaitSignalConfig configures a step to park a path until an external
// signal is delivered via the execution's SignalStore.
//
// The declarative counterpart of workflow.Wait. Use it when the step
// graph, not imperative activity code, is the right place to express
// "stop here until X arrives" — e.g., a gate before a production
// deploy, a human-in-the-loop approval, a callback from an async
// external system.
//
// Topic is a template evaluated at step-entry time against the
// current branch state; the resolved value is what the engine
// registers as the rendezvous key. Typical patterns:
//
//   - Static:   "approval-requested"
//   - Dynamic:  "callback-${state.request_id}"
//   - Expression: "${state.meta.correlation_id}"
//
// Store is the variable name that receives the signal payload when it
// arrives. Like Step.Store, a "state." prefix is stripped.
//
// Timeout is required and must be positive. A timeout with no
// OnTimeout routing fails the step with a WorkflowError of type
// ErrorTypeTimeout. A timeout with OnTimeout set routes the path to
// the named next step without failing.
type WaitSignalConfig struct {
	// Topic is a Risor-templated rendezvous key. Required.
	Topic string `json:"topic"`
	// Timeout is the maximum time to wait for the signal. Required.
	Timeout time.Duration `json:"timeout"`
	// Store is the path variable that receives the signal payload when
	// the signal is delivered. Optional.
	Store string `json:"store,omitempty"`
	// OnTimeout is the name of the step to route to when the wait
	// times out. When empty, a timeout fails the step.
	OnTimeout string `json:"on_timeout,omitempty"`
}

// SleepConfig configures a step that durably sleeps for a fixed
// wall-clock duration. The path hard-suspends — its goroutine exits,
// the checkpoint records an absolute WakeAt, and the execution ends
// dormant until a consumer resumes it at or after WakeAt.
//
// Sleep survives process restarts: on resume before WakeAt the path
// re-suspends; on resume at or after WakeAt the path wakes and
// advances to the successor step.
//
// When a sleeping path is paused via PauseBranch, the sleep clock
// freezes: the remaining duration is recorded on WaitState and the
// absolute WakeAt is cleared. On unpause, WakeAt is recomputed as
// now + remaining, so the pause period does not consume sleep time.
type SleepConfig struct {
	// Duration is the wall-clock duration the path should sleep.
	// Must be positive.
	Duration time.Duration `json:"duration"`
}

// JoinConfig configures a step to wait for multiple branches to converge.
type JoinConfig struct {
	// Branches specifies which named branches to wait for. If empty,
	// waits for all active branches.
	Branches []string `json:"branches,omitempty"`

	// Count specifies the number of branches to wait for. If 0, waits
	// for all specified branches.
	Count int `json:"count,omitempty"`

	// BranchMappings specifies where to store branch data. Supports two
	// syntaxes:
	//  1. Store entire branch state: "branchID": "destination"
	//     Example: "branchA": "results.branchA" stores all branchA
	//     variables under results.branchA.
	//  2. Extract specific variables: "branchID.variable": "destination"
	//     Example: "branchA.result": "extracted.value" stores only
	//     branchA.result under extracted.value.
	// Supports nested field extraction using dot notation for both
	// variable names and destinations.
	BranchMappings map[string]string `json:"branch_mappings,omitempty"`
}

// Step represents a single node in a workflow's step graph.
//
// # Step kinds
//
// A step has exactly one kind, selected by which of these mutually
// exclusive fields is set:
//
//   - Activity — invokes a registered activity by name. The default
//     kind; the only kind that produces a value via Store.
//   - Join — waits for one or more named branches to converge,
//     then merges their state per JoinConfig.BranchMappings.
//   - WaitSignal — parks the branch until an external signal is
//     delivered to a topic.
//   - Sleep — durably suspends the branch for a wall-clock duration.
//     Survives process restarts.
//   - Pause — declarative counterpart to PauseBranch; parks the
//     branch until an operator unpauses it.
//
// workflow.New rejects any step that sets more than one kind field
// with ErrInvalidStepKind, and any step that sets none with the
// implicit "activity" default — Activity may be empty only if a
// Sleep, Pause, Join, or WaitSignal is set.
//
// # Modifier fields
//
//   - Store — name of the variable to write the step result into.
//     Activity-kind only.
//   - Parameters — typed input passed to the activity (Activity-kind
//     only). Values may use ${...} templates.
//   - Each — fan-out loop over a list. The step is executed once
//     per item in a fresh sub-branch.
//   - Next — outgoing edges, evaluated against EdgeMatchingStrategy.
//   - EdgeMatchingStrategy — "all" (default; follow every matching
//     edge, branching the path) or "first" (follow only the first
//     match, single branch continues).
//   - Retry — per-error-class retry policy with backoff. Activity-kind
//     only; rejected on Sleep/Pause/Join/WaitSignal at workflow.New.
//   - Catch — per-error-class fallback routing. Activity-kind only;
//     same restriction as Retry.
//
// Mixing a modifier with an incompatible kind is rejected at
// validation time with ErrInvalidModifier.
type Step struct {
	Name                 string               `json:"name"`
	Description          string               `json:"description,omitempty"`
	Store                string               `json:"store,omitempty"`
	Activity             string               `json:"activity,omitempty"`
	Parameters           map[string]any       `json:"parameters,omitempty"`
	Each                 *Each                `json:"each,omitempty"`
	Join                 *JoinConfig          `json:"join,omitempty"`
	WaitSignal           *WaitSignalConfig    `json:"wait_signal,omitempty"`
	Sleep                *SleepConfig         `json:"sleep,omitempty"`
	Pause                *PauseConfig         `json:"pause,omitempty"`
	Next                 []*Edge              `json:"next,omitempty"`
	EdgeMatchingStrategy EdgeMatchingStrategy `json:"edge_matching_strategy,omitempty"`
	Retry                []*RetryConfig       `json:"retry,omitempty"`
	Catch                []*CatchConfig       `json:"catch,omitempty"`
}

// GetEdgeMatchingStrategy returns the edge matching strategy for this step,
// defaulting to "all" if not specified
func (s *Step) GetEdgeMatchingStrategy() EdgeMatchingStrategy {
	if s.EdgeMatchingStrategy == "" {
		return EdgeMatchingAll
	}
	return s.EdgeMatchingStrategy
}

// JitterStrategy defines the jitter strategy for retry delays
type JitterStrategy string

const (
	JitterNone JitterStrategy = "NONE"
	JitterFull JitterStrategy = "FULL"
)

// RetryConfig configures retry behavior for a step.
type RetryConfig struct {
	ErrorEquals    []string       `json:"error_equals,omitempty"`
	MaxRetries     int            `json:"max_retries,omitempty"`
	BaseDelay      time.Duration  `json:"base_delay,omitempty"`
	MaxDelay       time.Duration  `json:"max_delay,omitempty"`
	BackoffRate    float64        `json:"backoff_rate,omitempty"`
	JitterStrategy JitterStrategy `json:"jitter_strategy,omitempty"`
	Timeout        time.Duration  `json:"timeout,omitempty"`
}

// CatchConfig configures fallback behavior when errors occur
type CatchConfig struct {
	ErrorEquals []string `json:"error_equals"`
	Next        string   `json:"next"`
	Store       string   `json:"store,omitempty"`
}
