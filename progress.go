package workflow

// ProgressDetail carries intra-activity progress information.
// Message is human-readable; Data is machine-readable.
type ProgressDetail struct {
	// Message is a human-readable description of the current progress.
	Message string

	// Data is arbitrary structured data that consumers can use to
	// drive UIs, metrics, or logging. The library does not inspect
	// it.
	Data map[string]any
}
