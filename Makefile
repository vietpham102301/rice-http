GO ?= go

.PHONY: test test-debug bench bench-record cover lint tidy

## test: run all tests with the race detector, no cache
test:
	$(GO) test ./... -race -count=1

## test-debug: run all tests under the ricedebug build, which panics on use-after-release
test-debug:
	$(GO) test ./... -race -count=1 -tags ricedebug

## bench: quick benchmark run, one iteration, for CI smoke testing
bench:
	$(GO) test . ./bench/... -run '^$$' -bench . -benchmem -count=1

## bench-record: full benchmark run, stamped and written to bench/results/
## usage: make bench-record LABEL=M1-minimal-server
bench-record:
	./scripts/bench.sh $(LABEL)

## cover: test coverage summary
cover:
	$(GO) test ./... -coverprofile=coverage.out -covermode=atomic -count=1
	$(GO) tool cover -func=coverage.out | tail -1

## lint: formatting and vet
lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files"; exit 1)
	$(GO) vet ./...

## tidy: sync go.mod and go.sum
tidy:
	$(GO) mod tidy
