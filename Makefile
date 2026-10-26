.PHONY: run mcp build test smoke tidy

# .env in the working directory is loaded by the process when present.

run:
	go run ./cmd/server

mcp:
	go run ./cmd/mcp-server

build:
	mkdir -p bin
	go build -o bin/bill-snap ./cmd/server
	go build -o bin/bill-snap-mcp ./cmd/mcp-server

test:
	go test ./...

smoke:
	bash scripts/smoke.sh

tidy:
	go mod tidy
