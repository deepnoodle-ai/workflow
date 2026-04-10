package goexpr

import "context"

// ctx is the default context used by tests that do not care about
// cancellation. Tests that exercise cancellation declare their own
// local ctx and shadow this one.
var ctx = context.Background()
