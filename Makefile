# MedBook Makefile

BINARY = medbook

.PHONY: build run-migrate run-sync run-serve clean test

build:
	go build -o $(BINARY) ./cmd/

## First-time setup: migrate + sync
setup: build run-migrate run-sync

run-migrate: build
	./$(BINARY) migrate

run-sync: build
	./$(BINARY) sync

run-serve: build
	./$(BINARY) serve

stats: build
	./$(BINARY) stats

test:
	go test ./...

clean:
	rm -f $(BINARY)

# Build a static binary (useful for deployment)
build-static:
	CGO_ENABLED=0 go build -o $(BINARY) ./cmd/
