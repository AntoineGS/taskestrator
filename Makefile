.PHONY: build test lint e2e

build:
	go build -o bin/tsk ./cmd/tsk

test:
	go test ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"

e2e:
	go test -tags e2e -count=1 ./e2e/...
