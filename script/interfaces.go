package script

import (
	"context"
)

// Value represents the result of a script evaluation.
type Value interface {

	// Value returns the Go value for this value as an any
	Value() any

	// Items returns the items for this value as an array of any
	Items() ([]any, error)

	// String returns the string representation of this value
	String() string

	// IsTruthy returns true if this value is truthy
	IsTruthy() bool
}

// Script represents a compiled script that can be evaluated.
type Script interface {
	Evaluate(ctx context.Context, globals map[string]any) (Value, error)
}

// Compiler is an interface used to compile source code into a Script.
type Compiler interface {
	Compile(ctx context.Context, code string) (Script, error)
}
