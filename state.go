package workflow

import (
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RunStatus is the lifecycle position of a run.
type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusFailing   RunStatus = "failing" // on_failure steps are running
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

// Terminal reports whether the run has finished.
func (s RunStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// State is everything a run needs between events. It is JSON-serializable;
// the host stores it (for example in a jsonb column on the run row) and
// passes it back to [Apply]. Treat it as opaque except for the documented
// read-only fields.
type State struct {
	Definition string         `json:"definition"` // hash of the definition
	Status     RunStatus      `json:"status"`
	Inputs     map[string]any `json:"inputs,omitempty"`
	Trigger    map[string]any `json:"trigger,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at,omitzero"`
	// UpdatedAt is the time of the last event that changed the state.
	// Apply treats an event stamped earlier as happening at UpdatedAt, so
	// the run's clock never goes backwards.
	UpdatedAt time.Time      `json:"updated_at"`
	Outputs   map[string]any `json:"outputs,omitempty"`
	Error     *RunError      `json:"error,omitempty"`

	// Paths are the lines of execution, keyed by path ID.
	Paths map[string]*Path `json:"paths"`
	// Nodes are step visits, keyed by node key (a StepID without index
	// and attempt). A visit no expression can read any more, because a
	// newer visit of the same step superseded it, is dropped, so a loop
	// keeps only its latest passes. Hosts keep their own history.
	Nodes map[string]*Node `json:"nodes"`
	// Joins maps a join step to the paths waiting at it.
	Joins map[string][]string `json:"joins,omitempty"`
	// Bytes is the size of the outputs, set values and each items the
	// nodes in Nodes hold, as JSON. It is what Limits.MaxStateBytes caps.
	Bytes int `json:"bytes"`
}

// PathStatus is where a path is.
type PathStatus string

const (
	PathActive  PathStatus = "active"
	PathWaiting PathStatus = "waiting" // at a join
	PathEnded   PathStatus = "ended"
)

// Path is a line of execution. A path forks into child paths when more than
// one edge matches, and joins merge paths back into their common ancestor.
type Path struct {
	ID     string     `json:"id"`
	Parent string     `json:"parent,omitempty"`
	Status PathStatus `json:"status"`
	// Step and Node are where the path is. Node is empty while waiting at
	// a join.
	Step  string `json:"step,omitempty"`
	Node  string `json:"node,omitempty"`
	Ended string `json:"ended,omitempty"` // end, forked, joined, cancelled
	// Visits counts how many times the path's lineage has entered each
	// step.
	Visits map[string]int `json:"visits,omitempty"`
	// Scope maps each step name the path can read to the node holding its
	// value.
	Scope map[string]string `json:"scope,omitempty"`
	// Since holds the steps settled on this path's lineage. Re-entering a
	// step in a loop clears it and everything downstream of it.
	Since map[string]bool `json:"since,omitempty"`
	// Children are the paths the most recent fork created.
	Children []string `json:"children,omitempty"`
	// Absorbed are the paths a join merged into this path.
	Absorbed []string `json:"absorbed,omitempty"`
}

// NodeStatus is the state of a step visit.
type NodeStatus string

const (
	NodeRunning   NodeStatus = "running"
	NodeCompleted NodeStatus = "completed"
	NodeFailed    NodeStatus = "failed"
	NodeCancelled NodeStatus = "cancelled"
)

// Node is one visit of a step on a path.
type Node struct {
	Key     string     `json:"key"`
	Path    string     `json:"path"`
	Step    string     `json:"step"`
	Visit   int        `json:"visit"`
	Failure bool       `json:"failure,omitempty"` // an on_failure step
	Status  NodeStatus `json:"status"`
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
// settled, sorted.
func (s *State) InFlight() []StepID {
	var out []StepID
	for _, key := range sortedKeys(s.Nodes) {
		n := s.Nodes[key]
		for i, u := range n.Units {
			if u.Status == UnitRunning {
				out = append(out, makeStepID(key, n.unitIndex(i), u.Attempt))
			}
		}
	}
	return out
}

// Unit returns the unit a StepID names, or nil when the StepID is unknown or
// names an attempt that a retry has replaced. Hosts use it after Apply to
// record what became of an attempt, such as a completion that failed because
// its output was too large.
func (s *State) Unit(id StepID) *Unit {
	key, index, attempt, ok := id.parse()
	if !ok {
		return nil
	}
	n := s.Nodes[key]
	if n == nil {
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
	return n.Units[index]
}

// dropped reports whether a node key names a visit that was made and has
// since been dropped: its path's lineage has entered the step at least
// that many times. It cannot tell such a visit from one its lineage made
// on an ancestor path, under that path's key, so a StepID that names the
// wrong path for a real visit is ignored as late instead of reported as
// unknown.
func (s *State) dropped(def *Definition, key string) bool {
	slash := strings.LastIndexByte(key, '/')
	if slash < 0 {
		return false
	}
	pathID, step, visit := key[:slash], key[slash+1:], 1
	if at := strings.LastIndexByte(step, '@'); at >= 0 {
		v, err := strconv.Atoi(step[at+1:])
		if err != nil || v < 2 {
			return false
		}
		step, visit = step[:at], v
	}
	p := s.Paths[pathID]
	if p == nil || def.byName[step] == nil || nodeKey(pathID, step, visit) != key {
		return false
	}
	return visit <= p.Visits[step]
}

func (n *Node) unitIndex(i int) int {
	if n.Each {
		return i
	}
	return -1
}

func (s *State) clone() *State {
	c := *s
	c.Inputs = maps.Clone(s.Inputs)
	c.Trigger = maps.Clone(s.Trigger)
	c.Outputs = maps.Clone(s.Outputs)
	if s.Error != nil {
		e := *s.Error
		c.Error = &e
	}
	c.Paths = make(map[string]*Path, len(s.Paths))
	for k, p := range s.Paths {
		cp := *p
		cp.Visits = cloneMap(p.Visits)
		cp.Scope = cloneMap(p.Scope)
		cp.Since = cloneMap(p.Since)
		cp.Children = append([]string(nil), p.Children...)
		cp.Absorbed = append([]string(nil), p.Absorbed...)
		c.Paths[k] = &cp
	}
	c.Nodes = make(map[string]*Node, len(s.Nodes))
	for k, n := range s.Nodes {
		cn := *n
		cn.Units = make([]*Unit, len(n.Units))
		for i, u := range n.Units {
			cu := *u
			cn.Units[i] = &cu
		}
		c.Nodes[k] = &cn
	}
	c.Joins = make(map[string][]string, len(s.Joins))
	for k, v := range s.Joins {
		c.Joins[k] = append([]string(nil), v...)
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
	i := sort.SearchStrings(list, s)
	if i < len(list) && list[i] == s {
		return list
	}
	list = append(list, "")
	copy(list[i+1:], list[i:])
	list[i] = s
	return list
}
