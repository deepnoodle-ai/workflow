
EXPERIMENTAL_MODULES := \
	experimental/worker \
	experimental/store/postgres \
	experimental/store/sqlite

.PHONY: test
test:
	go test . ./activities ./script ./workflowtest

.PHONY: cover
cover:
	go test -coverprofile cover.out . ./activities ./script ./workflowtest
	go tool cover -html=cover.out

.PHONY: test-experimental
test-experimental:
	@set -e; for mod in $(EXPERIMENTAL_MODULES); do \
		echo "==> $$mod"; \
		(cd $$mod && go build ./... && go vet ./... && go test ./...); \
	done

.PHONY: test-all
test-all: test test-experimental
	go vet ./...
