
.PHONY: test
test:
	go test . ./activities ./script ./workflowtest
	cd scripts/risor && go test ./...
	cd scripts/expr && go test ./...

.PHONY: cover
cover:
	go test -coverprofile cover.out . ./activities ./script ./workflowtest
	go tool cover -html=cover.out

.PHONY: test-all
test-all: test
	cd cmd && go vet ./...
	cd examples && go vet ./...
