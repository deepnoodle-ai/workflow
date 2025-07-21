package workflow

import "time"

// ExecutionSummary provides a summary view of an execution
type ExecutionSummary struct {
	ExecutionID  string        `json:"execution_id"`
	WorkflowName string        `json:"workflow_name"`
	Status       string        `json:"status"`
	StartTime    time.Time     `json:"start_time"`
	EndTime      time.Time     `json:"end_time,omitempty"`
	Duration     time.Duration `json:"duration"`
	Error        string        `json:"error,omitempty"`
}
