.PHONY: test race vet build run

test:
	GOTOOLCHAIN=local CGO_ENABLED=0 go test ./... -count=1

race:
	GOTOOLCHAIN=local CGO_ENABLED=0 go test -race ./... -count=1

vet:
	GOTOOLCHAIN=local CGO_ENABLED=0 go vet ./...

build:
	GOTOOLCHAIN=local CGO_ENABLED=0 go build ./...

run:
	GOTOOLCHAIN=local CGO_ENABLED=0 go run ./cmd/server
