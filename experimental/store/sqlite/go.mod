module github.com/deepnoodle-ai/workflow/experimental/store/sqlite

go 1.26.1

require (
	github.com/deepnoodle-ai/workflow v0.0.0-00010101000000-000000000000
	github.com/deepnoodle-ai/workflow/experimental/worker v0.0.0-00010101000000-000000000000
)

require github.com/deepnoodle-ai/expr v0.0.1 // indirect

replace (
	github.com/deepnoodle-ai/workflow => ../../../
	github.com/deepnoodle-ai/workflow/experimental/worker => ../../worker
)
