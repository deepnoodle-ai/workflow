package workflow

// WorkflowFormatter interface for pretty output
type WorkflowFormatter interface {
	PrintStepStart(stepName string, activityName string)
	PrintStepOutput(stepName string, content any)
	PrintStepError(stepName string, err error)
}
