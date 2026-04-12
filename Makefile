
EXPERIMENTAL_MODULES := \
	experimental/worker \
	experimental/store/postgres \
	experimental/store/sqlite

.PHONY: all test cover test-experimental test-all clean

all: test-all

test:
	go test . ./activities ./script ./workflowtest

cover:
	go test -coverprofile cover.out . ./activities ./script ./workflowtest
	go tool cover -html=cover.out

test-experimental:
	@set -e; for mod in $(EXPERIMENTAL_MODULES); do \
		echo "==> $$mod"; \
		(cd $$mod && go build ./... && go vet ./... && go test ./...); \
	done

test-all: test test-experimental
	go vet ./...

clean:
	rm -f cover.out
