# Contributing to clibench

## Development Setup

```bash
git clone https://github.com/lykinsbd/clibench.git
cd clibench
make build
make test
```

## Running Tests

```bash
# All tests (no root needed)
make test

# Netem tests (requires root / CAP_NET_ADMIN)
make test-netem

# Lint
make lint  # requires golangci-lint v2
```

## Running Benchmarks

```bash
# Baseline (no latency, no root needed)
make bench-local

# Simulated 30ms RTT (requires root for tc netem)
make bench-regional

# Custom run
./bin/bench -latency continental -iterations 50 -commands 10
```

## Adding a Benchmark Mode

1. Add the mode logic in `cmd/bench/main.go` inside the appropriate
   `benchSSH`, `benchHTTPS`, or `benchProxy` function.
2. Use `stats.RunParallel` for iteration execution and `stats.Summarize`
   for result collection.
3. Update the README benchmark modes table.
4. Run `make test` and `make lint` before submitting.

## Adding a Command Transcript

Place a `.txt` file in `transcripts/` with underscores replacing spaces
in the command name. For example, `show_ip_route.txt` maps to the command
`show ip route`. Use `{{.Hostname}}` for hostname substitution.

## Submitting Changes

1. Fork the repo and create a feature branch.
2. Make your changes with tests.
3. Run `make test && make lint`.
4. Open a pull request against `main`.
