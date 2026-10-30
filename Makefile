.PHONY: all test test-long test-pg test-sqlite test-sqlite-mattn test-decisions test-parity cover test-all clean

all: test-all

test:
	go test ./...

# The randomized engine tests at full size; run after changing the engine.
test-long:
	WORKFLOW_LONG=1 go test -count=1 .

test-pg:
	cd postgres && go vet ./... && go test ./...

test-sqlite:
	cd sqlite && go vet ./... && go test ./...

# mattn/go-sqlite3 needs cgo; the build tag adds its harness.
test-sqlite-mattn:
	cd sqlite && CGO_ENABLED=1 go vet -tags sqlite_mattn ./... && \
		CGO_ENABLED=1 go test -tags sqlite_mattn ./...

test-decisions:
	cd decisions && go vet ./... && go test ./...

test-parity:
	cd internal/parity && go vet ./... && go test ./...

cover:
	go test -coverprofile cover.out .
	go tool cover -html=cover.out

test-all: test test-pg test-sqlite test-decisions test-parity
	go vet ./...

clean:
	rm -f cover.out
