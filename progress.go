package workflow

// ProgressDetail carries intra-activity progress information.
// Message is human-readable; Data is machine-readable.
type ProgressDetail struct {
	// Message is a human-readable description of the current progress.
	Message string

	// Data is arbitrary structured data that consumers can use to drive
	// UIs, metrics, or logging. The library does not inspect it.
	Data map[string]any
}

// ProgressReporter is an optional interface that workflow contexts may
// implement to support intra-activity progress reporting.
type ProgressReporter interface {
	ReportProgress(detail ProgressDetail)
}

// ReportProgress reports intra-activity progress. If the context supports
// progress reporting (i.e., a StepProgressStore is configured), the detail
// is forwarded to the store. Otherwise this is a no-op.
//
// Example:
//
//	workflow.ReportProgress(ctx, workflow.ProgressDetail{
//	    Message: "Processing batch 3 of 12",
//	    Data:    map[string]any{"batch": 3, "total": 12},
//	})
func ReportProgress(ctx Context, detail ProgressDetail) {
	if pr, ok := ctx.(ProgressReporter); ok {
		pr.ReportProgress(detail)
	}
}
