
.PHONY: cover
cover:
	go test -coverprofile cover.out . ./activities ./script
	go tool cover -html=cover.out
