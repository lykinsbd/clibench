# Contributing to clibench

Contributions are welcome! Please keep PRs focused and be kind in reviews.

## Prerequisites

- Go 1.22+
- Linux (for `tc netem` latency injection; userspace fallback works anywhere)
- [golangci-lint](https://golangci-lint.run/) v2:
  ```bash
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
  ```

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
make lint
```

## Running Benchmarks

```bash
# Baseline (no latency, no root needed)
make bench-local

# Simulated 30ms RTT (requires root for tc netem)
make bench-regional

# Custom run
./bin/clibench bench --latency continental --iterations 50 --commands 10

# Single transport
./bin/clibench bench --transport http3 --latency regional --iterations 20

# Multiple transports (comma-separated or repeated)
./bin/clibench bench --transport ssh,https --latency local --iterations 10

# Table output
./bin/clibench bench --latency local --iterations 20 --commands 5 --output table
```

## Regenerating Results

```bash
make results           # all (netem + userspace, requires sudo)
make results-netem     # netem only (requires sudo)
make results-userspace # userspace only (no root)
```

## CLI Structure

Single `clibench` binary with subcommands via [kong](https://github.com/alecthomas/kong).
CLI is defined as Go structs with struct tags in `cmd/clibench/`:

| File | Purpose |
|---|---|
| `main.go` | CLI struct + `kong.Parse` entrypoint |
| `bench.go` | `BenchCmd` — benchmark runner |
| `server.go` | `ServerCmd` — standalone server |
| `smoketest.go` | `SmoketestCmd` — integration smoke test |
| `format.go` | Table and CSV output formatters |

To add a flag, add a struct field with kong tags. Enum validation,
defaults, short flags, and help text are all struct tags.

## Adding a New Transport

1. Create the server package in `internal/` (e.g., `internal/myserver/`).
2. Create the benchmark function in `internal/bench/`.
3. Add the transport name to the `enum` tag on `BenchCmd.Transport` in `cmd/clibench/bench.go`.
4. Add server startup and benchmark execution in `BenchCmd.Run()`.
5. Add unit tests, benchmark tests, and integration tests verifying output equivalence.
6. Update the README benchmark modes table.

## Adding a Benchmark Mode

1. Add mode logic in the appropriate function in `internal/bench/`.
2. Use `stats.RunParallel` for iteration execution and `Config.summarize()` for results.
3. For HTTP-based servers, implement `httphandler.Runner` and use `httphandler.Mux()`.
4. For SSH-based servers, use `sshutil.ServerConfig()` and `sshutil.Serve()`.
5. Add a test in the corresponding `_test.go` file.
6. Update the README benchmark modes table.

## Adding a Command Transcript

Place a `.txt` file in `transcripts/` with underscores replacing spaces
in the command name (e.g., `show_ip_route.txt` → `show ip route`).
Use `{{.Hostname}}` for hostname substitution.

## Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/) style:

```
feat: add gNMI transport benchmark
fix: correct netem teardown on SIGINT
refactor: split tunnel into tunnel-https and tunnel-http3
docs: update latency profile table
```

## Submitting Changes

1. Fork the repo and create a feature branch.
2. Make your changes with tests.
3. Run `make test && make lint`.
4. Open a [pull request](https://github.com/lykinsbd/clibench/pulls) against `main`.
