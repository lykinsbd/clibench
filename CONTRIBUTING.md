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

## Regenerating Results

The `results/` directory contains sample JSON output used in the blog
series. To regenerate after code changes:

```bash
# All results — netem (requires sudo) + userspace fallback
make results

# Netem only (requires root / CAP_NET_ADMIN)
make results-netem

# Userspace only (no root needed)
make results-userspace
```

The script (`scripts/generate-results.sh`) runs all latency profiles
at n=20 with 5 commands, plus regional scaling runs at 1/10/25/50
commands. Takes about 5-10 minutes for the full set.

## Adding a Benchmark Mode

1. Add the mode logic in `internal/bench/bench.go` inside the
   appropriate `SSH`, `HTTPS`, or `Proxy` function.
2. Use `stats.RunParallel` for iteration execution and `stats.Summarize`
   for result collection.
3. Add a test in `internal/bench/bench_test.go`.
4. Update the README benchmark modes table.
5. Run `make test && make lint` before submitting.

## Adding a Command Transcript

Place a `.txt` file in `transcripts/` with underscores replacing spaces
in the command name. For example, `show_ip_route.txt` maps to the command
`show ip route`. Use `{{.Hostname}}` for hostname substitution.

## Submitting Changes

1. Fork the repo and create a feature branch.
2. Make your changes with tests.
3. Run `make test && make lint`.
4. Open a pull request against `main`.
