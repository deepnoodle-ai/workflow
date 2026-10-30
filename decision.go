package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/deepnoodle-ai/expr"
)

// KindDecision is the step kind of the jobs that answer decisions: the
// decide and choose calls in a step's next block. A host that registers
// it, with a handler under the same name, lets definitions use them. No
// step can have this kind.
const KindDecision = "decision"

// The decision functions.
const (
	fnDecide = "decide"
	fnChoose = "choose"
)

// Every decision uses this policy, never its step's: a step's retry and
// timeout are for its own work, which can take far longer than a model
// call, and a hung model must not hold a run.
var decisionRetry = &Retry{MaxAttempts: 3}

const decisionTimeout = time.Minute

// decisionCall is one decide or choose call in a step's next block.
type decisionCall struct {
	where    string // the edge, for messages: next page: when
	fn       string
	question string
	options  []string // choose only
	subject  *program
}

// decisionFunctions answer decide and choose from the saved answers in
// ctx. route puts them there; Load rejects the calls anywhere else.
var decisionFunctions = map[string]any{
	fnDecide: expr.Func(func(ctx context.Context, args []any) (any, error) {
		return answer(ctx, fnDecide, args)
	}),
	fnChoose: expr.Func(func(ctx context.Context, args []any) (any, error) {
		return answer(ctx, fnChoose, args)
	}),
}

type answersKey struct{}

func answer(ctx context.Context, fn string, args []any) (any, error) {
	d, _ := ctx.Value(answersKey{}).(*decisionState)
	if d == nil {
		return nil, fmt.Errorf("%s can only be used in next: when or switch", fn)
	}
	want := 2
	if fn == fnChoose {
		want = 3
	}
	if len(args) != want {
		return nil, fmt.Errorf("%s: got %d arguments, want %d", fn, len(args), want)
	}
	question, _ := args[0].(string)
	var options []string
	if fn == fnChoose {
		list, _ := args[2].([]any)
		for _, o := range list {
			s, _ := o.(string)
			options = append(options, s)
		}
	}
	text, err := subjectText(args[1])
	if err != nil {
		return nil, err
	}
	a := d.Answers[questionID(fn, question, text, options)]
	if a == nil || a.Value == nil {
		return nil, fmt.Errorf("%s(%q, ...) has no answer", fn, question)
	}
	return a.Value, nil
}

// subjectText is what the model sees: a string as it is, anything else
// as JSON with sorted keys.
func subjectText(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("the subject is not JSON: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// questionID names a question by everything that makes it one. Equal
// calls share it, so a question asked on two edges is asked once.
func questionID(fn, question, text string, options []string) string {
	data, _ := json.Marshal([]any{fn, question, text, options})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// compileDecisions collects the decision calls in the step's next block
// and checks their form.
func (s *Step) compileDecisions(p *problems) {
	add := func(where string, prog *program) {
		if prog == nil {
			return
		}
		for _, c := range prog.p.Calls(fnDecide, fnChoose) {
			call, msg := newDecisionCall(c)
			if msg != "" {
				p.add(s.Name, "%s: %s", where, msg)
				continue
			}
			call.where = where
			s.decisions = append(s.decisions, call)
		}
	}
	add("next: switch", s.swtch)
	for i, prog := range s.edges {
		add(fmt.Sprintf("next %s: when", s.Next[i].Step), prog)
	}
}

func newDecisionCall(c expr.Call) (decisionCall, string) {
	switch {
	case c.Name == fnDecide && len(c.Args) != 2:
		return decisionCall{}, "decide takes (question, subject)"
	case c.Name == fnChoose && len(c.Args) != 3:
		return decisionCall{}, "choose takes (question, subject, options)"
	case c.Bound:
		return decisionCall{}, c.Name +
			" cannot be used inside map, filter or another list function"
	}
	call := decisionCall{fn: c.Name}

	v, _ := c.Args[0].Literal()
	call.question, _ = v.(string)
	if call.question == "" {
		return decisionCall{}, c.Name + ": the question must be a string literal"
	}
	if c.Name == fnChoose {
		v, _ := c.Args[2].Literal()
		list, _ := v.([]any)
		for _, o := range list {
			s, ok := o.(string)
			if !ok || slices.Contains(call.options, s) {
				list = nil
				break
			}
			call.options = append(call.options, s)
		}
		if len(list) == 0 {
			return decisionCall{}, "choose: the options must be a list of distinct string literals"
		}
	}
	if len(c.Args[1].Calls(fnDecide, fnChoose)) > 0 {
		return decisionCall{}, c.Name + ": the subject cannot ask another decision"
	}
	call.subject = &program{src: c.Args[1].Source(), p: c.Args[1]}
	return call, ""
}

// question is one question of a decision job.
type question struct {
	id   string
	text string
	call decisionCall
}

// questions evaluates the subjects of the step's decision calls on the
// path, and returns one question per distinct ID, sorted by ID.
func (r *run) questions(s *Step, p *pathState) ([]question, *RunError) {
	env := r.env(p, nil)
	byID := map[string]question{}
	for _, c := range s.decisions {
		v, err := c.subject.eval(env)
		if err == nil {
			var text string
			if text, err = subjectText(v); err == nil {
				id := questionID(c.fn, c.question, text, c.options)
				byID[id] = question{id: id, text: text, call: c}
				continue
			}
		}
		return nil, &RunError{
			Step:    s.Name,
			Type:    ErrorTypeExpression,
			Message: fmt.Sprintf("%s: %s(%q, ...): subject: %v", c.where, c.fn, c.question, err),
		}
	}
	out := make([]question, 0, len(byID))
	for _, id := range sortedKeys(byID) {
		out = append(out, byID[id])
	}
	return out, nil
}

// ask starts the decision job for a node whose step completed.
func (r *run) ask(n *nodeState, p *pathState) {
	s := r.def.byName[n.Step]
	qs, rerr := r.questions(s, p)
	if rerr != nil {
		r.failRun(rerr)
		return
	}
	d := &decisionState{Answers: make(map[string]*answerState, len(qs))}
	for _, q := range qs {
		d.Answers[q.id] = &answerState{
			Type:     q.call.fn,
			Question: q.call.question,
			Options:  q.call.options,
		}
	}
	n.Decision = d
	if err := r.charge(n, jsonSize(d.Answers)); err != nil {
		d.Unit.Error = asStepError(err)
		r.decisionFailed(n, d.Unit.Error)
		return
	}
	if err := r.dispatchDecision(n, p, time.Time{}); err != nil {
		r.failRun(err)
	}
}

// dispatchDecision emits the decision job's next attempt. It evaluates
// the subjects again, as dispatch evaluates with: on every attempt; the
// path's scope does not change while it waits, so neither do they.
func (r *run) dispatchDecision(n *nodeState, p *pathState, notBefore time.Time) *RunError {
	d := n.Decision
	attempt := d.Unit.Attempt + 1
	dispatch, timer, rerr := r.decisionCommands(n, p, attempt, notBefore)
	if rerr != nil {
		return rerr
	}
	d.Unit.Attempt = attempt
	d.Unit.Status = UnitRunning
	d.Unit.DispatchedAt = r.at
	d.Unit.NotBefore = notBefore
	r.emit(dispatch)
	r.emit(timer)
	return nil
}

// decisionCommands builds an attempt's DispatchStep and its timeout.
func (r *run) decisionCommands(
	n *nodeState, p *pathState, attempt int, notBefore time.Time,
) (dispatch, timer Command, rerr *RunError) {
	s := r.def.byName[n.Step]
	qs, rerr := r.questions(s, p)
	if rerr != nil {
		return Command{}, Command{}, rerr
	}
	list := make([]any, 0, len(qs))
	schema := &Schema{
		Type:          []string{"object"},
		Properties:    make(map[string]*Schema, len(qs)),
		AdditionalSet: true,
	}
	for _, q := range qs {
		item := map[string]any{
			"id":       q.id,
			"type":     q.call.fn,
			"question": q.call.question,
			"subject":  q.text,
		}
		prop := &Schema{Type: []string{"number"}}
		if q.call.fn == fnChoose {
			opts := make([]any, len(q.call.options))
			for i, o := range q.call.options {
				opts[i] = o
			}
			item["options"] = opts
			prop = &Schema{Type: []string{"string"}, Enum: opts}
		}
		list = append(list, item)
		schema.Properties[q.id] = prop
		schema.Required = append(schema.Required, q.id)
	}

	dispatch = Command{
		Kind:         CommandDispatchStep,
		StepID:       makeDecisionID(n.Key, attempt),
		Step:         s.Name,
		StepKind:     KindDecision,
		Params:       map[string]any{"questions": list},
		Index:        -1,
		Attempt:      attempt,
		NotBefore:    notBefore,
		OutputSchema: schema,
	}
	start := r.at
	if notBefore.After(start) {
		start = notBefore
	}
	timer = Command{
		Kind:      CommandScheduleTimer,
		StepID:    dispatch.StepID,
		Step:      s.Name,
		StepKind:  KindDecision,
		Index:     -1,
		Attempt:   attempt,
		NotBefore: start.Add(decisionTimeout),
	}
	return dispatch, timer, nil
}

// decisionCompleted checks and saves the answers, then routes.
func (r *run) decisionCompleted(n *nodeState, output any) {
	d := n.Decision
	v, size, err := normalize(output)
	if err != nil {
		r.decisionFailedAttempt(n, &StepError{Type: ErrorTypeInvalidOutput, Message: err.Error()})
		return
	}
	if problems := checkAnswers(d, v); len(problems) > 0 {
		r.decisionFailedAttempt(n, &StepError{
			Type:    ErrorTypeInvalidOutput,
			Message: "decision answers are not valid: " + strings.Join(problems, "; "),
		})
		return
	}
	if err := r.checkOutput(size); err != nil {
		r.decisionFailedAttempt(n, err)
		return
	}
	// ask charged the questions; charge only what the answers add, so
	// each answer counts once.
	before := jsonSize(d.Answers)
	setAnswers(d, v.(map[string]any))
	if err := r.charge(n, jsonSize(d.Answers)-before); err != nil {
		setAnswers(d, nil)
		r.decisionFailedAttempt(n, asStepError(err))
		return
	}
	d.Unit.Status, d.Unit.Output, d.Unit.Error = UnitCompleted, v, nil
	r.later(func() { r.route(n.Path, n.Key) })
}

func setAnswers(d *decisionState, answers map[string]any) {
	for id, a := range d.Answers {
		a.Value = answers[id]
	}
}

// checkAnswers checks a decision job's output: one answer for each
// question, a number from 0 to 1 for decide and an option for choose.
func checkAnswers(d *decisionState, v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("want an object of answers by question id, got %s",
			describe(v))}
	}
	var out []string
	for _, id := range sortedKeys(d.Answers) {
		a := d.Answers[id]
		got, ok := m[id]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("no answer for %s (%q)", id, a.Question))
		case a.Type == fnDecide:
			if f, isNum := got.(float64); !isNum || f < 0 || f > 1 {
				out = append(out, fmt.Sprintf("%s: want a number from 0 to 1, got %s",
					id, describe(got)))
			}
		default:
			if s, isStr := got.(string); !isStr || !slices.Contains(a.Options, s) {
				out = append(out, fmt.Sprintf("%s: want one of %s, got %s",
					id, strings.Join(a.Options, ", "), describe(got)))
			}
		}
	}
	for _, id := range sortedKeys(m) {
		if d.Answers[id] == nil {
			out = append(out, fmt.Sprintf("%s: no such question", id))
		}
	}
	return out
}

// decisionFailedAttempt tries a failed decision again: a decision gets 3
// attempts, any failure uses one, retryable or not, and after the third the
// step fails.
func (r *run) decisionFailedAttempt(n *nodeState, err *StepError) {
	d := n.Decision
	if d.Unit.Attempt < decisionRetry.MaxAttempts {
		d.Unit.Error = err
		p := r.st.paths[n.Path]
		notBefore := r.at.Add(decisionRetry.backoff(d.Unit.Attempt))
		if rerr := r.dispatchDecision(n, p, notBefore); rerr == nil {
			return
		} else {
			err = &StepError{Type: rerr.Type, Message: rerr.Message}
		}
	}
	r.decisionFailed(n, err)
}

// decisionFailed fails the step the decision leaves, as if its own work
// had failed: its value becomes the error, and its catch applies.
func (r *run) decisionFailed(n *nodeState, err *StepError) {
	d := n.Decision
	d.Unit.Status, d.Unit.Error = UnitFailed, err
	var id StepID
	if d.Unit.Attempt > 0 {
		id = makeDecisionID(n.Key, d.Unit.Attempt)
	}
	r.failNode(n, id, err)
}

// cancelDecision stops a decision in flight.
func (r *run) cancelDecision(n *nodeState) {
	d := n.Decision
	if d == nil || d.Unit.Status != UnitRunning {
		return
	}
	r.emit(Command{
		Kind:     CommandCancelStep,
		StepID:   makeDecisionID(n.Key, d.Unit.Attempt),
		Step:     n.Step,
		StepKind: KindDecision,
		Index:    -1,
		Attempt:  d.Unit.Attempt,
	})
	d.Unit.Status = UnitCancelled
}

// decisionState is the job that answers the decisions in a step's next
// block, once the step completed.
type decisionState struct {
	Unit    Unit                    `json:"unit"`
	Answers map[string]*answerState `json:"answers"` // by question ID
}

// answerState is one question and, once the job completed, its answer.
// The subject is not kept: a replay computes the same one from state.
type answerState struct {
	Type     string   `json:"type"` // decide or choose
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Value    any      `json:"value"`
}

func (d *decisionState) clone() *decisionState {
	if d == nil {
		return nil
	}
	c := &decisionState{Unit: d.Unit, Answers: make(map[string]*answerState, len(d.Answers))}
	for id, a := range d.Answers {
		c.Answers[id] = new(*a)
	}
	return c
}
