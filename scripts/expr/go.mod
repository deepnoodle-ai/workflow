module github.com/deepnoodle-ai/workflow/scripts/expr

go 1.25

require (
	github.com/deepnoodle-ai/workflow v0.0.0
	github.com/expr-lang/expr v1.17.7
	github.com/stretchr/testify v1.10.0
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/deepnoodle-ai/workflow => ../..
