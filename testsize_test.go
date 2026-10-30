package workflow

import "os"

// sized returns full when WORKFLOW_LONG=1, else short. By default the
// randomized tests run a small sample so that `go test` and CI stay fast;
// set WORKFLOW_LONG=1 after changing the engine to run them all. It reads
// the variable on each call, while a test runs, so that go test's cache
// keys on it.
func sized(short, full int) int {
	if os.Getenv("WORKFLOW_LONG") == "1" {
		return full
	}
	return short
}
