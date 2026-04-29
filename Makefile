.PHONY: build test test-netem lint bench-local bench-regional clean

build:
	go build -o bin/bench ./cmd/bench
	go build -o bin/server ./cmd/server
	go build -o bin/smoketest ./cmd/smoketest

test:
	go test -race -count=1 -timeout 120s ./...

test-netem: ## Requires root / CAP_NET_ADMIN
	sudo go test -race -v -tags netem_root ./internal/netem/

lint:
	golangci-lint run

bench-local: build
	./bin/bench -latency local -iterations 20 -commands 5

bench-regional: build ## Requires root for tc netem
	sudo ./bin/bench -latency regional -iterations 20 -commands 5

results: build ## Regenerate all benchmark results (netem + userspace)
	./scripts/generate-results.sh all

results-netem: build ## Regenerate netem results only (requires root)
	./scripts/generate-results.sh netem

results-userspace: build ## Regenerate userspace results only
	./scripts/generate-results.sh userspace

clean:
	rm -rf bin/ coverage.out
