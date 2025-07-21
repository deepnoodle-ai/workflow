# Workflow

An easy-to-use workflow automation library for Go. Supports conditional
branching, parallel execution, checkpointing, and embedded scripting with [Risor](https://risor.io).

## Main Concepts

### Workflows

A **Workflow** defines a repeatable process as a directed graph of steps. Each workflow has:

- **Steps**: Individual tasks that perform work
- **Activities**: Functions that execute the actual work
- **Edges**: Define flow between steps with optional conditions
- **State**: Shared data that persists between steps

### Steps & Activities

**Steps** are the nodes in your workflow graph. Each step:

- Executes an **Activity** (built-in or custom function)
- Can store results in shared state
- Defines next steps with conditional logic
- Supports parameters with template variable substitution

### State & Templates

The workflow maintains shared state accessible via template variables:

- `${state.variable_name}` - Access stored values
- `${inputs.param_name}` - Access workflow inputs
- Template variables are evaluated at runtime

## Quick Example

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/activities"
)

func main() {
    // Define workflow
    wf, err := workflow.New(workflow.Options{
        Name: "data-processing",
        Steps: []*workflow.Step{
            {
                Name:     "Get Current Time",
                Activity: "time.now",
                Store:    "start_time",
                Next:     []*workflow.Edge{{Step: "Process Data"}},
            },
            {
                Name:     "Process Data", 
                Activity: "script",
                Parameters: map[string]any{
                    "code": `"Processing started at " + state.start_time`,
                },
                Store: "message",
                Next:  []*workflow.Edge{{Step: "Print Result"}},
            },
            {
                Name:     "Print Result",
                Activity: "print",
                Parameters: map[string]any{
                    "message": "${state.message}",
                },
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    // Create execution
    execution, err := workflow.NewExecution(workflow.ExecutionOptions{
        Workflow: wf,
        Inputs:   map[string]any{},
        Activities: []workflow.Activity{
            workflow.NewActivityFunction("time.now", func(ctx context.Context, params map[string]any) (any, error) {
                return time.Now().Format(time.RFC3339), nil
            }),
            &activities.ScriptActivity{},
            &activities.PrintActivity{},
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    // Execute workflow
    if err := execution.Run(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```
