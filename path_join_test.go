package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestPathJoining(t *testing.T) {
	t.Run("basic path joining with variable extraction", func(t *testing.T) {
		// Create a workflow with path joining that extracts specific variables
		wf, err := New(Options{
			Name: "path-join-test",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "setup",
					Store:    "value",
					Next: []*Edge{
						{Step: "work_a", Path: "a"},
						{Step: "work_b", Path: "b"},
						{Step: "join", Path: "final"},
					},
				},
				{
					Name:     "work_a",
					Activity: "double",
					Store:    "result",
				},
				{
					Name:     "work_b",
					Activity: "triple",
					Store:    "result",
				},
				{
					Name: "join",
					Join: &JoinConfig{
						Paths: []string{"a", "b"},
						PathMappings: map[string]string{
							"a.result": "doubled",
							"b.result": "tripled",
						},
					},
					Next: []*Edge{{Step: "combine"}},
				},
				{
					Name:     "combine",
					Activity: "sum",
					Store:    "total",
				},
			},
			Outputs: []*Output{
				{Name: "total", Variable: "total", Path: "final"},
				{Name: "doubled", Variable: "doubled", Path: "final"},
				{Name: "tripled", Variable: "tripled", Path: "final"},
			},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return 10, nil
				}),
				NewActivityFunction("double", func(ctx Context, params map[string]any) (any, error) {
					value, _ := ctx.GetVariable("value")
					return value.(int) * 2, nil
				}),
				NewActivityFunction("triple", func(ctx Context, params map[string]any) (any, error) {
					value, _ := ctx.GetVariable("value")
					return value.(int) * 3, nil
				}),
				NewActivityFunction("sum", func(ctx Context, params map[string]any) (any, error) {
					doubled, _ := ctx.GetVariable("doubled")
					tripled, _ := ctx.GetVariable("tripled")
					return doubled.(int) + tripled.(int), nil
				}),
			},
		})
		assert.NoError(t, err)

		// Run the workflow
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), ExecutionStatusCompleted)

		// Verify outputs
		outputs := execution.GetOutputs()
		assert.Equal(t, outputs["total"], 50)   // 20 + 30
		assert.Equal(t, outputs["doubled"], 20) // 10 * 2
		assert.Equal(t, outputs["tripled"], 30) // 10 * 3
	})

	t.Run("path joining with full path state storage", func(t *testing.T) {
		// Test storing entire path state rather than extracting specific variables
		wf, err := New(Options{
			Name: "full-state-join-test",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "setup",
					Store:    "base",
					Next: []*Edge{
						{Step: "process_x", Path: "x"},
						{Step: "process_y", Path: "y"},
						{Step: "collect", Path: "final"},
					},
				},
				{
					Name:     "process_x",
					Activity: "work_x",
					Store:    "x_data",
				},
				{
					Name:     "process_y",
					Activity: "work_y",
					Store:    "y_data",
				},
				{
					Name: "collect",
					Join: &JoinConfig{
						Paths: []string{"x", "y"},
						PathMappings: map[string]string{
							"x": "path_x",
							"y": "path_y",
						},
					},
					Next: []*Edge{{Step: "analyze"}},
				},
				{
					Name:     "analyze",
					Activity: "combine",
					Store:    "result",
				},
			},
			Outputs: []*Output{
				{Name: "result", Variable: "result", Path: "final"},
			},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return 5, nil
				}),
				NewActivityFunction("work_x", func(ctx Context, params map[string]any) (any, error) {
					ctx.SetVariable("x_meta", "x_processed")
					base, _ := ctx.GetVariable("base")
					return base.(int) * 4, nil
				}),
				NewActivityFunction("work_y", func(ctx Context, params map[string]any) (any, error) {
					ctx.SetVariable("y_meta", "y_processed")
					base, _ := ctx.GetVariable("base")
					return base.(int) * 6, nil
				}),
				NewActivityFunction("combine", func(ctx Context, params map[string]any) (any, error) {
					pathX, _ := ctx.GetVariable("path_x")
					pathY, _ := ctx.GetVariable("path_y")

					pathXMap := pathX.(map[string]any)
					pathYMap := pathY.(map[string]any)

					// Verify full path state was captured
					assert.Equal(t, pathXMap["x_meta"], "x_processed")
					assert.Equal(t, pathYMap["y_meta"], "y_processed")
					assert.Equal(t, pathXMap["x_data"], 20) // 5 * 4
					assert.Equal(t, pathYMap["y_data"], 30) // 5 * 6

					return pathXMap["x_data"].(int) + pathYMap["y_data"].(int), nil
				}),
			},
		})
		assert.NoError(t, err)

		// Run the workflow
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), ExecutionStatusCompleted)

		// Verify the result
		outputs := execution.GetOutputs()
		assert.Equal(t, outputs["result"], 50) // 20 + 30
	})
}
