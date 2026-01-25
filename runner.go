package workflow

import "github.com/deepnoodle-ai/workflow/domain"

// Runners define how activities are executed by workers. They convert
// activity parameters to task specifications and interpret results.
//
// Available runner implementations live in the runners package.

// Runner defines how an activity is executed by workers.
type Runner = domain.Runner
