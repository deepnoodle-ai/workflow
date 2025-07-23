# Workflow

An easy-to-use workflow automation library for Go. Supports conditional
branching, parallel execution, embedded scripting, and execution checkpointing.

Think of it like a lightweight hybrid of Temporal and AWS Step Functions.

When defining steps, you have access to string templating and scripting features
using the [Risor](https://risor.io) language.

## Main Concepts

| Concept | Description |
|---------|-------------|
| **Workflow**   | A repeatable process defined as a directed graph of steps |
| **Steps**      | Individual nodes in the workflow graph |
| **Activities** | Functions that perform the actual work |
| **Edges**      | Define flow between steps |
| **Execution**  | A single run of a workflow |
| **State**      | Shared mutable state that persists for the duration of an execution |

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
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func main() {

	attempt := 0

	myOperation := func(ctx workflow.Context, input map[string]any) (string, error) {
		attempt++
		if attempt < 3 { // Simulated failure
			return "", fmt.Errorf("service is temporarily unavailable")
		}
		return "SUCCESS", nil
	}

	w, err := workflow.New(workflow.Options{
		Name: "demo",
		Steps: []*workflow.Step{
			{
				Name:     "Call My Operation",
				Activity: "my_operation",
				Store:    "result",
				Retry:    []*workflow.RetryConfig{{MaxRetries: 2}},
				Next:     []*workflow.Edge{{Step: "Finish"}},
			},
			{
				Name:     "Finish",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🎉 Workflow completed successfully! Result: ${state.result}",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: w,
		Logger:   workflow.NewLogger(),
		Activities: []workflow.Activity{
			workflow.NewTypedActivityFunction("my_operation", myOperation),
			activities.NewPrintActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := execution.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```
