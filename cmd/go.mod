module github.com/deepnoodle-ai/workflow/cmd

go 1.25

require (
	github.com/deepnoodle-ai/workflow v0.0.0
	github.com/deepnoodle-ai/workflow/scriptengines/risor v0.0.0
	github.com/fatih/color v1.18.0
)

require (
	github.com/deepnoodle-ai/risor/v2 v2.1.0 // indirect
	github.com/deepnoodle-ai/wonton v0.0.25 // indirect
	github.com/gofrs/uuid/v5 v5.3.2 // indirect
	github.com/lmittmann/tint v1.1.2 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	go.jetify.com/typeid v1.3.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/deepnoodle-ai/workflow => ..
	github.com/deepnoodle-ai/workflow/scriptengines/risor => ../scriptengines/risor
)
