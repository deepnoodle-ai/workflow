# Workflow

An easy-to-use workflow automation library for Go. Supports conditional
branching, parallel execution, embedded scripting, and execution checkpointing.

Think of it like a lightweight hybrid of Temporal and AWS Step Functions.

When defining steps, you have access to string templating and scripting features
using the [Risor](https://risor.io) language.

## Main Concepts

| Concept | Description |
|---------|-------------|
| **Workflow** | A repeatable process defined as a directed graph of steps |
| **Steps** | Individual nodes in the workflow graph |
| **Activities** | Functions that perform the actual work |
| **Edges** | Define flow between steps |
| **Execution** | A single run of a workflow |
| **State** | Shared mutable state that persists for the duration of an execution |

### How They Work Together

**Workflows** define **Steps** that execute **Activities**. An **Execution** is
a single run of a workflow. When a step finishes, its outgoing **Edges** are
evaluated and the next step(s) are selected based on any associated conditions.
The **State** may be read and written to by the activities.

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
