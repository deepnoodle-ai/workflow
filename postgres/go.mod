module github.com/deepnoodle-ai/workflow/postgres

go 1.26.1

require (
	github.com/deepnoodle-ai/workflow v0.0.0-00010101000000-000000000000
	github.com/deepnoodle-ai/workflow/worker v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.7.1
)

require (
	github.com/deepnoodle-ai/expr v0.0.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.27.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/text v0.18.0 // indirect
)

replace (
	github.com/deepnoodle-ai/workflow => ../
	github.com/deepnoodle-ai/workflow/worker => ../worker
)
