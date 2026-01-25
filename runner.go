package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/task"
)

// Runners define how activities are executed by workers. They convert
// activity parameters to task specifications and interpret results.
//
// Available runners:
//   - InlineRunner: Executes activities in-process (testing/simple activities)
//   - ContainerRunner: Executes activities as Docker containers
//   - ProcessRunner: Executes activities as local processes
//   - HTTPRunner: Executes activities by calling HTTP endpoints

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
