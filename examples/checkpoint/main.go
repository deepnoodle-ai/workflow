// Command checkpoint makes a CLI release durable with no database: the
// runner's Save writes the state to a JSON file on every change, and the
// next invocation loads that file and resumes the run with Runner.Resume.
//
// The program plays two invocations of the same CLI in a temp dir. The
// first is interrupted during deploy, as by Ctrl-C. The second finds the
// state file and continues: it runs deploy again (claims=2, because the
// first attempt may have done part of the work) and verify, but not backup
// or migrate.
//
//	go run ./examples/checkpoint
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var doc []byte

func main() {
	dir, err := os.MkdirTemp("", "checkpoint")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "release-1.4.0.json")

	fmt.Println("== invocation 1")
	release(path, "deploy")
	fmt.Println("\n== invocation 2")
	release(path, "")
}

// release is the CLI's main: it starts the release, or resumes it if a
// state file exists. interruptAt is the step during which the process is
// interrupted, for the demo.
func release(path, interruptAt string) {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil})
	if err != nil {
		log.Fatal(err)
	}
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()

	action := func(ctx context.Context, job workflow.Job) (any, error) {
		fmt.Printf("   %-16s %s claims=%d\n", job.StepID, job.Config["action"], job.Claims)
		if job.Step == interruptAt {
			interrupt() // Ctrl-C while deploy is rolling out
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return map[string]any{"ok": true}, nil
	}
	r := &local.Runner{
		Handlers: map[string]workflow.Handler{"action": action},
		Save: func(ctx context.Context, runID string, st *workflow.State) error {
			data, err := json.Marshal(st)
			if err != nil {
				return err
			}
			// Write then rename, so a crash never leaves half a file.
			if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
				return err
			}
			return os.Rename(path+".tmp", path)
		},
	}

	var run *local.Run
	if data, err := os.ReadFile(path); err == nil {
		var st *workflow.State
		if err := json.Unmarshal(data, &st); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("   resuming: %s, in flight %v\n", st.Status, st.InFlight())
		run, err = r.Resume(ctx, "release-1.4.0", def, st)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		run, err = r.Start(ctx, "release-1.4.0", def, map[string]any{"version": "1.4.0"})
		if err != nil {
			log.Fatal(err)
		}
	}

	st, err := run.Wait()
	switch {
	case errors.Is(err, context.Canceled):
		// Stopped, not cancelled: the state file still says running.
		fmt.Printf("   interrupted: %s, in flight %v\n", st.Status, st.InFlight())
	case err != nil:
		log.Fatal(err)
	default:
		fmt.Printf("   run %s\n", st.Status)
	}
}
