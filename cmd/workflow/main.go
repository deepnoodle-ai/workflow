// Command workflow validates and runs workflow definitions locally.
//
//	workflow validate review.json
//	workflow run review.json -input repo=deepnoodle-ai/workflow -trigger event.json
//
// The CLI knows three step kinds: print (writes with.message to stdout),
// shell (runs the config value as a command and returns its output) and
// echo (returns with: as its output).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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

var handlers = map[string]local.Handler{
	"print": func(_ context.Context, c workflow.Command) (any, error) {
		fmt.Println(c.Params["message"])
		return nil, nil
	},
	"echo": func(_ context.Context, c workflow.Command) (any, error) {
		return c.Params, nil
	},
	"shell": func(ctx context.Context, c workflow.Command) (any, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", c.Config["shell"].(string))
		for k, v := range c.Params {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%v", strings.ToUpper(k), v))
		}
		cmd.Env = append(cmd.Env, os.Environ()...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, &workflow.StepError{Type: "shell", Message: err.Error(), Retryable: true}
		}
		return strings.TrimSpace(out.String()), nil
	},
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
	var inputs inputFlags = map[string]any{}
	fs.Var(&inputs, "input", "input as key=value (repeatable; JSON values allowed)")
	triggerPath := fs.String("trigger", "", "JSON file read as `event`")
	asJSON := fs.Bool("json", false, "print the final state as JSON")
	_ = fs.Parse(args)

	def, err := load(path)
	if err != nil {
		fail(err)
	}
	var trigger map[string]any
	if *triggerPath != "" {
		data, err := os.ReadFile(*triggerPath)
		if err != nil {
			fail(err)
		}
		if err := json.Unmarshal(data, &trigger); err != nil {
			fail(fmt.Errorf("trigger: %w", err))
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := (&local.Runner{Handlers: handlers}).Run(ctx, def, inputs, trigger)
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
	def, err := workflow.Parse(data)
	if err != nil {
		return nil, err
	}
	return def, def.Validate(kinds)
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
	fmt.Fprintln(os.Stderr, "usage: workflow validate FILE | workflow run FILE [-input k=v]... [-trigger FILE] [-json]")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
