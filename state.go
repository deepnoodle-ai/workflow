package workflow

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RunStatus is the lifecycle position of a run.
type RunStatus string

// A run is running until its outcome is decided: every path ended
// (completed), a step failed (failed) or a cancel arrived (cancelled). It
// then finishes, after its on_failure steps if it failed and its finally
// steps in any case. While those run, its status is the outcome's
// "-ing" form: completing, failing or cancelling. The terminal command is
// emitted when they are done.
//
// An operator can pause a running run: its status is paused until it is
// resumed (running again), cancelled, or its outcome is decided. Apply
// advances a paused run as it does a running one; the host does not start
// its dispatched steps (see EventPause).
const (
	StatusRunning RunStatus = "running"
	// StatusPaused: an operator paused the run. Steps in flight finish;
	// the host starts no dispatched step until the run leaves paused.
	StatusPaused RunStatus = "paused"
	// StatusCompleting: the run completed; finally steps are running.
	StatusCompleting RunStatus = "completing"
	// StatusFailing: the run failed; on_failure steps, then finally steps,
	// are running.
	StatusFailing RunStatus = "failing"
	// StatusCancelling: the run was cancelled; finally steps are running.
	StatusCancelling RunStatus = "cancelling"
	StatusCompleted  RunStatus = "completed"
	StatusFailed     RunStatus = "failed"
	StatusCancelled  RunStatus = "cancelled"
)

// Terminal reports whether the run has finished.
func (s RunStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// Outcome is the terminal status a run has or is finishing with:
// completed for completing, failed for failing, cancelled for cancelling,
// the status itself when it is terminal, and "" while it is running or
// paused.
func (s RunStatus) Outcome() RunStatus {
	switch s {
	case StatusCompleting, StatusCompleted:
		return StatusCompleted
	case StatusFailing, StatusFailed:
		return StatusFailed
	case StatusCancelling, StatusCancelled:
		return StatusCancelled
	}
	return ""
}

// progressing reports whether the run is in its main phase: running, or
// paused, which the host holds and Apply does not.
func (s RunStatus) progressing() bool {
	return s == StatusRunning || s == StatusPaused
}

// finishing returns the status of a run finishing with outcome.
func finishing(outcome RunStatus) RunStatus {
	switch outcome {
	case StatusCompleted:
		return StatusCompleting
	case StatusFailed:
		return StatusFailing
	}
	return StatusCancelling
}

// State is everything a run needs between events. It is JSON-serializable;
// the host stores it (for example in a jsonb column on the run row) and
// passes it back to [Apply].
//
// The exported fields and methods are the supported read view: the run's
// status, input, outputs, error and timestamps, [State.InFlight] and
// [State.Unit]. Everything else (paths, step visits, joins) is the
// engine's bookkeeping. It is serialized with the state but is not API,
// and it is not a history: visits a loop has superseded are dropped.
// Hosts keep their own record of attempts, as the postgres host's steps
// table does.
type State struct {
	// DefinitionHash is the hash of the definition the run started with.
	DefinitionHash string         `json:"definition"`
	Status         RunStatus      `json:"status"`
	Input          map[string]any `json:"input,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at,omitzero"`
	// UpdatedAt is the time of the last event that changed the state.
	// Apply treats an event stamped earlier as happening at UpdatedAt, so
	// the run's clock never goes backwards.
	UpdatedAt time.Time      `json:"updated_at"`
	Outputs   map[string]any `json:"outputs,omitempty"`
	Error     *RunError      `json:"error,omitempty"`
	// Reason is why the run is paused, from the pause event, or why it was
	// cancelled, from the cancel event.
	Reason string `json:"reason,omitempty"`
	// FinallyErrors lists the finally steps that failed, or did not run
	// because a cancel stopped the finally steps, in order. They do not
	// change the run's outcome; the terminal command carries them too.
	FinallyErrors []*RunError `json:"finally_errors,omitempty"`
	// Bytes is the size of the outputs, set values and each items the run
	// holds, as JSON. It is what Limits.MaxStateBytes caps.
	Bytes int `json:"bytes"`

	// paths are the lines of execution, keyed by path ID.
	paths map[string]*pathState
	// nodes are step visits, keyed by node key (a StepID without index
	// and attempt). A visit no expression can read any more, because a
	// newer visit of the same step superseded it, is dropped, so a loop
	// keeps only its latest passes.
	nodes map[string]*nodeState
	// joins maps a join step to the paths waiting at it.
	joins map[string][]string
	// retired is the merged scope of the paths that were dropped: for
	// each step, the latest visit a dropped path could read. Outputs,
	// on_failure and finally read it with the paths' scopes.
	retired map[string]string
	// timeouts holds the attempts whose step timeout fired while the run
	// was paused, by StepID: zero until Resume re-arms the timeout, then
	// the new deadline. An entry goes when its attempt settles.
	timeouts map[string]time.Time
	// reported is the visit of the attempt the last applied event reported
	// on, when that Apply dropped it, so Unit can still say what became of
	// the attempt. It is not stored: a reloaded state has no last event.
	reported *nodeState
}

// stateFields is State's exported fields, for JSON.
type stateFields State

// stateJSON is how a State is stored: its exported fields and its
// bookkeeping side by side.
type stateJSON struct {
	*stateFields
	Paths map[string]*pathState `json:"paths"`
	Nodes map[string]*nodeState `json:"nodes"`
	Joins map[string][]string   `json:"joins,omitempty"`
	// Retired is State.retired.
	Retired map[string]string `json:"retired,omitempty"`
	// Timeouts is State.timeouts.
	Timeouts map[string]time.Time `json:"timeouts,omitempty"`
}

// MarshalJSON encodes the state with the engine's bookkeeping, which
// [Apply] needs back.
func (s State) MarshalJSON() ([]byte, error) {
	return json.Marshal(stateJSON{
		stateFields: (*stateFields)(&s),
		Paths:       s.paths,
		Nodes:       s.nodes,
		Joins:       s.joins,
		Retired:     s.retired,
		Timeouts:    s.timeouts,
	})
}

// UnmarshalJSON decodes a state that MarshalJSON encoded.
func (s *State) UnmarshalJSON(data []byte) error {
	v := stateJSON{stateFields: (*stateFields)(s)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	s.paths, s.nodes, s.joins, s.retired = v.Paths, v.Nodes, v.Joins, v.Retired
	s.timeouts = v.Timeouts
	return nil
}

// pathStatus is where a path is.
type pathStatus string

const (
	pathActive  pathStatus = "active"
	pathWaiting pathStatus = "waiting" // at a join
	pathEnded   pathStatus = "ended"
)

// Path is a line of execution. When more than one edge matches, the path
// continues along one of them and starts a child path for each other, and
// joins merge paths back together.
type pathState struct {
	ID     string     `json:"id"`
	Parent string     `json:"parent,omitempty"`
	Status pathStatus `json:"status"`
	// Step and Node are where the path is. Node is empty while waiting at
	// a join.
	Step  string `json:"step,omitempty"`
	Node  string `json:"node,omitempty"`
	Ended string `json:"ended,omitempty"` // end, joined, cancelled
	// Visits counts how many times the path's lineage has entered each
	// step.
	Visits map[string]int `json:"visits,omitempty"`
	// Scope maps each step name the path can read to the node holding its
	// value.
	Scope map[string]string `json:"scope,omitempty"`
	// Settled holds the steps settled on this path's lineage, each with
	// the path's pass of every loop the step is in when it settled. Joins
	// use it to check that a named step settled in the current pass.
	Settled map[string][]int `json:"settled,omitempty"`
	// Seq counts the child paths this path started, per segment (the
	// step a child starts at, or the join that started it). Only this
	// path allocates IDs under its own, so an ID is never handed out
	// twice, even once its record is dropped.
	Seq map[string]int `json:"seq,omitempty"`
	// Via maps each step this path forked at to the target it left along:
	// the branch that brought it back, if it comes back. A child starts
	// with the target it was started for. The next fork there stays on
	// it (see carrier), so a loop whose returning branch is not the one
	// the graph prefers moves to a child path once, not on every pass.
	Via map[string]string `json:"via,omitempty"`
}

// nodeStatus is the state of a step visit.
type nodeStatus string

const (
	nodeRunning   nodeStatus = "running"
	nodeCompleted nodeStatus = "completed"
	nodeFailed    nodeStatus = "failed"
	nodeCancelled nodeStatus = "cancelled"
)

// Node is one visit of a step on a path.
type nodeState struct {
	Key   string `json:"key"`
	Path  string `json:"path"`
	Step  string `json:"step"`
	Visit int    `json:"visit"`
	// Pass is the path's pass of every loop when the node was entered:
	// with Visit, it orders visits of a step (see compareVisits).
	Pass    []int      `json:"pass,omitempty"`
	Failure bool       `json:"failure,omitempty"` // an on_failure step
	Finally bool       `json:"finally,omitempty"` // a finally step
	Status  nodeStatus `json:"status"`
	Each    bool       `json:"each,omitempty"`
	// Units are the dispatchable pieces of work: one per each item, or one
	// for a plain step.
	Units []*Unit `json:"units,omitempty"`
	// Failing is set when an each item failed; the node then fails,
	// cancelling the items in flight.
	Failing   bool       `json:"failing,omitempty"`
	Output    any        `json:"output,omitempty"`
	Error     *StepError `json:"error,omitempty"`
	EnteredAt time.Time  `json:"entered_at"`
	SettledAt time.Time  `json:"settled_at,omitzero"`
	// Bytes is the size of the values this node holds, counted in
	// State.Bytes.
	Bytes int `json:"bytes,omitempty"`
	// Decision is the job that answers the decisions in the step's next
	// block, once the step completed.
	Decision *decisionState `json:"decision,omitempty"`
	// Approval is the job that asks a person to approve the step's held
	// attempt, once an attempt was held.
	Approval *approvalState `json:"approval,omitempty"`
}

// job returns the decision or approval job a StepID suffix names, or nil
// when the node has none.
func (n *nodeState) job(suffix idSuffix) *Unit {
	switch {
	case suffix == suffixDecision && n.Decision != nil:
		return &n.Decision.Unit
	case suffix == suffixApproval && n.Approval != nil:
		return &n.Approval.Unit
	}
	return nil
}

// UnitStatus is the state of one unit of work.
type UnitStatus string

const (
	UnitPending   UnitStatus = "pending" // not dispatched yet (each concurrency)
	UnitRunning   UnitStatus = "running"
	UnitCompleted UnitStatus = "completed"
	UnitFailed    UnitStatus = "failed"
	UnitCancelled UnitStatus = "cancelled"
	UnitSkipped   UnitStatus = "skipped" // never dispatched because the step failed
)

// Unit is one dispatchable piece of work. Only the latest attempt is kept.
type Unit struct {
	Item         any        `json:"item,omitempty"`
	Attempt      int        `json:"attempt,omitempty"`
	Status       UnitStatus `json:"status"`
	NotBefore    time.Time  `json:"not_before,omitzero"`
	DispatchedAt time.Time  `json:"dispatched_at,omitzero"`
	Output       any        `json:"output,omitempty"`
	Error        *StepError `json:"error,omitempty"`
}

// InFlight returns the StepIDs of every dispatched unit that has not
// settled, sorted by node, then by item; a node's decision or approval job
// comes after its units. A held attempt has settled: its approval job is
// in flight instead.
func (s *State) InFlight() []StepID {
	var out []StepID
	for _, key := range sortedKeys(s.nodes) {
		n := s.nodes[key]
		for i, u := range n.Units {
			if u.Status == UnitRunning && !n.held(i) {
				out = append(out, makeStepID(key, n.unitIndex(i), u.Attempt))
			}
		}
		if d := n.Decision; d != nil && d.Unit.Status == UnitRunning {
			out = append(out, makeDecisionID(key, d.Unit.Attempt))
		}
		if a := n.Approval; a != nil && a.Unit.Status == UnitRunning {
			out = append(out, makeApprovalID(key, a.Unit.Attempt))
		}
	}
	return out
}

// Unit returns the unit a StepID names, or nil when the StepID is unknown or
// names an attempt that a retry has replaced. Hosts use it after Apply to
// record what became of an attempt, such as a completion that failed because
// its output was too large. That works even when the same Apply dropped the
// attempt's visit for a later one, on the state Apply returned (not on a
// reloaded copy).
//
// A held attempt (see [Approval]) is returned as failed, with the error
// that blocked it: nothing more is wanted from it while its approval job
// runs.
func (s *State) Unit(id StepID) *Unit {
	key, index, attempt, suffix, ok := id.parse()
	if !ok {
		return nil
	}
	n := s.nodes[key]
	if n == nil && s.reported != nil && s.reported.Key == key {
		n = s.reported
	}
	if n == nil {
		return nil
	}
	if suffix != suffixNone {
		if u := n.job(suffix); u != nil && u.Attempt == attempt {
			return u
		}
		return nil
	}
	if !n.Each {
		if index >= 0 {
			return nil
		}
		index = 0
	}
	if index < 0 || index >= len(n.Units) || n.Units[index].Attempt != attempt {
		return nil
	}
	if n.held(index) {
		u := *n.Units[index]
		u.Status = UnitFailed
		return &u
	}
	return n.Units[index]
}

// dropped reports whether a node key names a visit that was made and has
// since been dropped: its path's lineage has entered the step at least
// that many times, or its path was allocated and has since been dropped
// (see issued). It cannot tell such a visit from one its lineage made on
// an ancestor path, under that path's key, so a StepID that names the
// wrong path for a real visit is ignored as late instead of reported as
// unknown. The StepID's index and suffix must still fit the step, and the
// step must be in the section its path runs.
func (s *State) dropped(def *Definition, key string, index int, suffix idSuffix) bool {
	pathID, step, found := strings.CutLast(key, "/")
	if !found {
		return false
	}
	visit := 1
	if name, n, ok := strings.Cut(step, "@"); ok {
		v, err := strconv.Atoi(n)
		if err != nil || v < 2 {
			return false
		}
		step, visit = name, v
	}
	if !def.canSettle(pathID, step, index, suffix) || nodeKey(pathID, step, visit) != key {
		return false
	}
	p := s.paths[pathID]
	if p == nil {
		return s.issued(def, pathID)
	}
	return visit <= p.Visits[step]
}

// canSettle reports whether a result event can name an attempt of step on
// pathID: a decision of a step with decide or choose edges, an approval of
// a step with approval, or an attempt of a host, wait or sleep step with an
// item index exactly when the step has each; and the step is in the
// section the path runs.
func (d *Definition) canSettle(pathID, step string, index int, suffix idSuffix) bool {
	s := d.byName[step]
	if s == nil {
		return false
	}
	switch suffix {
	case suffixDecision:
		if len(s.decisions) == 0 {
			return false
		}
	case suffixApproval:
		if s.Approval == nil {
			return false
		}
	default:
		if !s.attempts() && s.Kind != KindSleep || (index >= 0) != (s.Each != "") {
			return false
		}
	}

	root, _, _ := strings.Cut(pathID, ".")
	switch root {
	case failurePath:
		return slices.Contains(d.OnFailure, s)
	case finallyPath:
		return slices.Contains(d.Finally, s)
	}
	return !slices.Contains(d.OnFailure, s) && !slices.Contains(d.Finally, s)
}

// issued reports whether a path ID was allocated in this run. The nearest
// path whose record is kept counts the children it started (Seq), so the
// segment below it is checked exactly; the segments below that only have
// to name steps, since the paths that allocated them are gone.
func (s *State) issued(def *Definition, id string) bool {
	if s.paths[id] != nil {
		return true
	}
	parent, seg, found := strings.CutLast(id, ".")
	if !found {
		return false
	}
	name, n, ok := parseSegment(def, seg)
	if !ok {
		return false
	}
	if p := s.paths[parent]; p != nil {
		return n <= p.Seq[name]
	}
	return s.issued(def, parent)
}

// parseSegment splits a path ID segment, step or step~N with N > 1, into
// the step and N (1 without a suffix).
func parseSegment(def *Definition, seg string) (string, int, bool) {
	name, suffix, found := strings.Cut(seg, "~")
	n := 1
	if found {
		v, err := strconv.Atoi(suffix)
		if err != nil || v < 2 || strconv.Itoa(v) != suffix {
			return "", 0, false
		}
		n = v
	}
	if def.byName[name] == nil {
		return "", 0, false
	}
	return name, n, true
}

func (n *nodeState) unitIndex(i int) int {
	if n.Each {
		return i
	}
	return -1
}

func (s *State) clone() *State {
	c := *s
	c.Input = maps.Clone(s.Input)
	c.Outputs = maps.Clone(s.Outputs)
	if s.Error != nil {
		c.Error = new(*s.Error)
	}
	c.FinallyErrors = slices.Clone(s.FinallyErrors)
	c.paths = make(map[string]*pathState, len(s.paths))
	for k, p := range s.paths {
		cp := *p
		cp.Visits = cloneMap(p.Visits)
		cp.Scope = cloneMap(p.Scope)
		cp.Settled = cloneMap(p.Settled)
		cp.Seq = cloneMap(p.Seq)
		cp.Via = cloneMap(p.Via)
		c.paths[k] = &cp
	}
	c.nodes = make(map[string]*nodeState, len(s.nodes))
	for k, n := range s.nodes {
		cn := *n
		cn.Units = make([]*Unit, len(n.Units))
		for i, u := range n.Units {
			cn.Units[i] = new(*u)
		}
		cn.Decision = n.Decision.clone()
		cn.Approval = n.Approval.clone()
		c.nodes[k] = &cn
	}
	c.joins = make(map[string][]string, len(s.joins))
	for k, v := range s.joins {
		c.joins[k] = slices.Clone(v)
	}
	c.retired = cloneMap(s.retired)
	c.reported = nil
	if s.timeouts != nil {
		c.timeouts = maps.Clone(s.timeouts)
	}
	return &c
}

// cloneMap copies a map, returning an empty map for nil: maps that were
// empty come back nil from JSON.
func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return maps.Clone(m)
}

func insertSorted(list []string, s string) []string {
	i, found := slices.BinarySearch(list, s)
	if found {
		return list
	}
	return slices.Insert(list, i, s)
}
