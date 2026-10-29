package workflow

import "os"

// longTests runs the randomized tests at full size. By default they run a
// small sample so that `go test` and CI stay fast; set WORKFLOW_LONG=1
// after changing the engine to run them all.
var longTests = os.Getenv("WORKFLOW_LONG") != ""

// sized returns full under WORKFLOW_LONG, else short.
func sized(short, full int) int {
	if longTests {
		return full
	}
	return short
}
