package local_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

// An operator pauses a run while its charge step calls a payment API that
// is down. The call fails, and its retry waits for Resume.
func ExampleRun_Pause() {
	def, err := workflow.Load([]byte(`{
	  "steps": [{"name": "charge", "action": "payments.charge",
	             "retry": {"max_attempts": 2, "backoff": "10ms"}}]
	}`), workflow.KindMap{"action": nil})
	if err != nil {
		panic(err)
	}

	var (
		mu      sync.Mutex
		called  []workflow.StepID
		started = make(chan struct{})
		release = make(chan struct{})
		held    = make(chan struct{})
		once    sync.Once
	)
	runner := &local.Runner{
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				mu.Lock()
				called = append(called, job.StepID)
				mu.Unlock()
				if job.Attempt == 1 {
					close(started)
					<-release
					return nil, &workflow.StepError{Type: "down", Retryable: true}
				}
				return "charged", nil
			},
		},
		// Save sees every state: here, the retry dispatched while paused.
		Save: func(_ context.Context, _ string, st *workflow.State) error {
			paused := st.Status == workflow.StatusPaused
			if paused && slices.Contains(st.InFlight(), "main/charge#2") {
				once.Do(func() { close(held) })
			}
			return nil
		},
	}

	run, err := runner.Start(context.Background(), "order-123", def, nil)
	if err != nil {
		panic(err)
	}
	<-started
	_ = run.Pause("payment API down")
	close(release) // the first attempt fails while the run is paused
	<-held
	time.Sleep(50 * time.Millisecond) // well past the 10ms backoff

	mu.Lock()
	fmt.Println("while paused:", called)
	mu.Unlock()

	_ = run.Resume()
	st, _ := run.Wait()
	fmt.Println("after resume:", called, st.Status)
	// Output:
	// while paused: [main/charge#1]
	// after resume: [main/charge#1 main/charge#2] completed
}
