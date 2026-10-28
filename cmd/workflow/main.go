// Command workflow validates and runs workflow definitions locally.
//
//	workflow validate review.json
//	workflow run review.json -input-file input.json -input repo=deepnoodle-ai/workflow
//
// The CLI knows three step kinds: print (writes with.message to stderr),
// shell (runs the config value with sh -c and returns its trimmed stdout;
// each with: parameter is set as an upper-cased environment variable,
// overriding the inherited environment) and echo (returns with: as its
// output).
//
// stdout carries only the result: the run's outputs as JSON, or with -json
// the final state. Step output (print, a shell command's stderr) and the
// run's status go to stderr, so `workflow run -json x.json | jq` works.
// Interrupting the CLI (Ctrl-C) cancels the run.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

var kinds = workflow.KindMap{
	"print": nil,
	"echo":  nil,
	"shell": func(cfg map[string]any) error {
		if _, ok := cfg["shell"].(string); !ok {
			return fmt.Errorf("shell must be a command string")
		}
		return nil
	},
}

var handlers = newHandlers(os.Stderr)

// newHandlers returns the CLI's handlers; print writes to out.
func newHandlers(out io.Writer) map[string]workflow.Handler {
	return map[string]workflow.Handler{
		"print": func(_ context.Context, c workflow.Job) (any, error) {
			fmt.Fprintln(out, c.Params["message"])
			return nil, nil
		},
		"echo": func(_ context.Context, c workflow.Job) (any, error) {
			return c.Params, nil
		},
		"shell": func(ctx context.Context, c workflow.Job) (any, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", c.Config["shell"].(string))
			// Params come after the inherited environment, so they win
			// (exec keeps the last value of a duplicated key).
			cmd.Env = os.Environ()
			for _, k := range slices.Sorted(maps.Keys(c.Params)) {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%v", strings.ToUpper(k), c.Params[k]))
			}
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = out
			if err := cmd.Run(); err != nil {
				return nil, &workflow.StepError{Type: "shell", Message: err.Error(), Retryable: true}
			}
			return strings.TrimSpace(stdout.String()), nil
		},
	}
}

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	switch os.Args[1] {
	case "validate":
		if _, err := load(os.Args[2]); err != nil {
			fail(err)
		}
		fmt.Println("ok")
	case "run":
		run(os.Args[2], os.Args[3:])
	default:
		usage()
	}
}

func run(path string, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var fields inputFlags = map[string]any{}
	fs.Var(&fields, "input",
		"set a top-level field of the input as key=value (repeatable; JSON values allowed)")
	inputPath := fs.String("input-file", "", "JSON object file read as the run's `input`")
	asJSON := fs.Bool("json", false, "print the final state as JSON on stdout instead of the outputs")
	_ = fs.Parse(args)

	def, err := load(path)
	if err != nil {
		fail(err)
	}
	input := map[string]any{}
	if *inputPath != "" {
		data, err := os.ReadFile(*inputPath)
		if err != nil {
			fail(err)
		}
		if err := json.Unmarshal(data, &input); err != nil {
			fail(fmt.Errorf("input-file: want a JSON object: %w", err))
		}
		if input == nil {
			input = map[string]any{}
		}
	}
	maps.Copy(input, fields)
	runID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	r, err := (&local.Runner{Handlers: handlers}).Start(context.Background(), runID, def, input)
	if err != nil {
		fail(err)
	}
	// An interrupt cancels the run, so its handlers stop and it reports
	// cancelled. Its finally steps still run; a second interrupt quits.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	go func() {
		select {
		case <-interrupt:
		case <-r.Done():
			return
		}
		signal.Stop(interrupt)
		msg := "interrupted: cancelling the run (interrupt again to quit)"
		if len(def.Finally) > 0 {
			msg = "interrupted: cancelling the run; its finally steps still run (interrupt again to quit)"
		}
		fmt.Fprintln(os.Stderr, msg)
		_ = r.Cancel("interrupted")
	}()
	st, err := r.Wait()
	signal.Stop(interrupt)
	if err != nil {
		fail(err)
	}
	if *asJSON {
		out, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(out))
	} else {
		out, _ := json.MarshalIndent(st.Outputs, "", "  ")
		fmt.Fprintf(os.Stderr, "%s\n", st.Status)
		fmt.Println(string(out))
	}
	for _, fe := range st.FinallyErrors {
		fmt.Fprintln(os.Stderr, "finally:", fe)
	}
	if st.Status != workflow.StatusCompleted {
		if st.Error != nil {
			fmt.Fprintln(os.Stderr, st.Error)
		}
		os.Exit(1)
	}
}

func load(path string) (*workflow.Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return workflow.Load(data, kinds)
}

type inputFlags map[string]any

func (f inputFlags) String() string { return fmt.Sprint(map[string]any(f)) }

func (f inputFlags) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("want key=value")
	}
	var parsed any
	if json.Unmarshal([]byte(v), &parsed) == nil {
		f[k] = parsed
	} else {
		f[k] = v
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: workflow validate FILE | workflow run FILE [-input-file FILE] [-input k=v]... [-json]

-input-file reads the run's input from a JSON object; each -input sets one
top-level field of it, overriding the file (the value is parsed as JSON if
it can be, else taken as a string).

run prints the outputs as JSON on stdout (the final state with -json).
Step output (print, shell stderr) and the run's status go to stderr.
with: parameters of a shell step become upper-cased environment variables
that override the inherited environment.`)
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
