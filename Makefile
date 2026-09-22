.PHONY: check fmt vet lint build test ci

fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	go vet ./...

lint:
	golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 ./...

build:
	go build ./...

test:
	go test -race ./...

# Single entry point for local checks and (once this project has a git
# remote) CI: fail fast, cheapest checks first.
check: fmt vet lint build test

ci: check
