package activities

import (
	"context"

	"github.com/deepnoodle-ai/workflow"
)

func newTestContext() workflow.Context {
	return workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
		PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{}),
	})
}
