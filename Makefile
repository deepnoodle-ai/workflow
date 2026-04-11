
.PHONY: test
test:
	go test . ./activities ./script ./workflowtest

.PHONY: cover
cover:
	go test -coverprofile cover.out . ./activities ./script ./workflowtest
	go tool cover -html=cover.out

.PHONY: test-all
test-all: test
	go vet ./...
