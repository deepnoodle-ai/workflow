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

// An operator pauses a run while its first step runs. The step finishes,
// but the next one waits for Resume.
func ExampleRun_Pause() {
	def, err := workflow.Load([]byte(`{
	  "steps": [
	    {"name": "fetch", "action": "http.get", "next": "charge"},
	    {"name": "charge", "action": "payments.charge"}
	  ]
	}`), workflow.KindMap{"action": nil})
	if err != nil {
		panic(err)
	}

	var (
		mu      sync.Mutex
		called  []string
		started = make(chan struct{})
		release = make(chan struct{})
		held    = make(chan struct{})
		once    sync.Once
	)
	runner := &local.Runner{
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				mu.Lock()
				called = append(called, job.Step)
				mu.Unlock()
				if job.Step == "fetch" {
					close(started)
					<-release
				}
				return nil, nil
			},
		},
		// Save sees every state: here, charge dispatched while paused.
		Save: func(_ context.Context, _ string, st *workflow.State) error {
			paused := st.Status == workflow.StatusPaused
			if paused && slices.Contains(st.InFlight(), "main/charge#1") {
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
	close(release) // fetch finishes while the run is paused
	<-held
	time.Sleep(20 * time.Millisecond) // charge would have started by now

	mu.Lock()
	fmt.Println("while paused:", called)
	mu.Unlock()

	_ = run.Resume()
	st, _ := run.Wait()
	fmt.Println("after resume:", called, st.Status)
	// Output:
	// while paused: [fetch]
	// after resume: [fetch charge] completed
}
