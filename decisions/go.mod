module github.com/deepnoodle-ai/workflow/decisions

go 1.27.0

require (
	github.com/deepnoodle-ai/decide v0.4.0
	github.com/deepnoodle-ai/workflow v0.1.0
)

require github.com/deepnoodle-ai/risor/v3 v3.0.0 // indirect

replace github.com/deepnoodle-ai/workflow => ../

replace github.com/deepnoodle-ai/risor/v3 => ../../risor/go
