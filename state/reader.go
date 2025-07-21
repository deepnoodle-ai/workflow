package state

// Reader provides read-only access to execution state
type Reader interface {
	// GetVariables returns a copy of the variables map
	GetVariables() map[string]any

	// GetInputs returns a copy of the inputs map
	GetInputs() map[string]any
}
