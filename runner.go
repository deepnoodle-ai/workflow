package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/task"
)

// Runner types - re-exported from internal/task for backwards compatibility.
// New code should use internal/task directly.

// Runner defines how an activity is executed by workers.
type Runner = task.Runner

// ContainerRunner executes activities as Docker containers.
type ContainerRunner = task.ContainerRunner

// ProcessRunner executes activities as local processes.
type ProcessRunner = task.ProcessRunner

// HTTPRunner executes activities by calling HTTP endpoints.
type HTTPRunner = task.HTTPRunner

// InlineRunner executes activities in-process using a function.
type InlineRunner = task.InlineRunner
