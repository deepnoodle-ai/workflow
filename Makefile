.PHONY: all test test-pg cover test-all clean

all: test-all

test:
	go test ./...

test-pg:
	cd postgres && go vet ./... && go test ./...

cover:
	go test -coverprofile cover.out .
	go tool cover -html=cover.out

test-all: test test-pg
	go vet ./...

clean:
	rm -f cover.out
