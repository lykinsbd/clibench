# clibench

[![CI](https://github.com/lykinsbd/clibench/actions/workflows/ci.yml/badge.svg)](https://github.com/lykinsbd/clibench/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/lykinsbd/clibench)](https://goreportcard.com/report/github.com/lykinsbd/clibench)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

SSH vs HTTPS CLI transport benchmark. Companion code for the
[CLI Over HTTPS](https://network-notes.com/posts/2026/cli-over-https-1/)
blog series on network-notes.com. Measures the performance difference between
SSH, HTTPS, and HTTP/3 (QUIC) as CLI transports for network device automation at scale.

## What This Does

A multi-protocol network device emulator and benchmark client. The server
emulates a Cisco IOS-XE device over:

- **SSH** (port 2222) — `crypto/ssh` with exec mode, following
  [CiSSHGo](https://github.com/tbotnz/cisshgo) patterns
- **HTTPS** (port 8443) — TLS 1.3 + ASA-style HTTP interface
  (`/admin/exec/`, `/admin/config`)
- **HTTP/3** (port 8444/udp) — QUIC + HTTP/3 with the same ASA-style
  endpoints, including 0-RTT session resumption support
- **Proxy** (port 9443) — HTTPS frontend that forwards to an SSH backend,
  simulating the edge proxy pattern

All transports share the same command engine and transcript responses, so
the only variable is the transport protocol itself.

## Quick Start

Requires Go 1.22+ and Linux with `tc` (iproute2). Root (or `CAP_NET_ADMIN`)
is needed for kernel-level latency injection.

```bash
git clone https://github.com/lykinsbd/clibench.git
cd clibench
go build -o bin/clibench ./cmd/clibench/

# Baseline (no latency, no root needed)
./bin/clibench bench --latency local --iterations 50 --commands 5

# Simulated US backbone (30ms RTT, requires root)
sudo ./bin/clibench bench --latency regional --iterations 20 --commands 5

# Simulated US↔Hong Kong (150ms RTT)
sudo ./bin/clibench bench --latency intercontinental --iterations 20 --commands 5

# Single transport
sudo ./bin/clibench bench --latency regional --iterations 20 --commands 5 --transport http3

# Table output for quick comparison
./bin/clibench bench --latency local --iterations 20 --commands 5 --output table

# CSV output for spreadsheet import
./bin/clibench bench --latency local --iterations 20 --commands 5 --output csv

# Fallback: userspace delay injection (no root, less accurate)
./bin/clibench bench --latency regional --iterations 20 --commands 5 --userspace

# Standalone server (SSH + HTTPS + HTTP/3)
./bin/clibench server

# Quick smoke test
./bin/clibench smoketest
```

Output is JSON to stdout. Logs go to stderr.

## Benchmark Modes

| Mode | Transport | Description |
|---|---|---|
| `fresh-conn` | SSH | New TCP + SSH handshake + auth per iteration (exec mode) |
| `reuse-conn` | SSH | Shared connection, new channel per command (exec mode, ControlMaster-style) |
| `batch-exec` | SSH | All commands in one exec payload |
| `pty-fresh` | SSH | New connection + PTY/shell per iteration (Netmiko-style prompt detection) |
| `pty-reuse` | SSH | Shared connection, new PTY/shell per iteration |
| `fresh-conn` | HTTPS | New TCP + TLS handshake per iteration |
| `keep-alive` | HTTPS | Shared TLS connection across all iterations |
| `batch-post` | HTTPS | All commands in one POST body |
| `multi-cmd` | HTTPS | All commands in one GET (ASA `/cmd1/cmd2` syntax) |
| `fresh-ssh` | Proxy | HTTPS→proxy→fresh SSH per request |
| `pooled-ssh` | Proxy | HTTPS→proxy→pooled SSH connection |
| `fresh-conn` | HTTP/3 | New QUIC + HTTP/3 connection per iteration |
| `keep-alive` | HTTP/3 | Shared QUIC connection across all iterations |
| `batch-post` | HTTP/3 | All commands in one POST body over shared connection |
| `0rtt-resumption` | HTTP/3 | QUIC 0-RTT session resumption (send data in first packet) |

## Latency Profiles

| Profile | RTT | Represents | Source |
|---|---|---|---|
| `local` | 0ms | Co-located / loopback | Baseline |
| `campus` | 2ms | Same data center / campus | AWS intra-AZ |
| `regional` | 30ms | Intra-country backbone | Verizon Mar 2026: US 29.9ms |
| `continental` | 70ms | Transatlantic | Verizon Mar 2026: 70.2ms |
| `intercontinental` | 150ms | US↔East Asia | Verizon Mar 2026: HK-US 145.5ms |
| `transpacific` | 175ms | US↔Australia/NZ | Verizon Mar 2026: NZ 174.2ms |

Source: [Verizon Enterprise Monthly IP Latency Statistics](https://www.verizon.com/business/terms/latency/)

## Methodology and Caveats

### How latency injection works (default: tc netem)

By default, the benchmark uses Linux `tc netem` to inject delay at the
kernel level on the loopback interface. Qdiscs and filters are configured
entirely via the [`vishvananda/netlink`](https://github.com/vishvananda/netlink)
library (the same netlink library used by Docker and Kubernetes) — no
shell-out to `tc`. A `prio` qdisc routes traffic by port:

- **WAN ports** (SSH, HTTPS, HTTP/3, proxy frontend): configured one-way delay
  (e.g., 15ms for 30ms RTT)
- **Campus port** (proxy backend SSH): fixed 1ms one-way delay (2ms RTT),
  simulating a co-located proxy
- **All other loopback traffic**: unaffected (default band, no delay)

Because `netem` operates in the kernel's network stack, it captures real
TCP behavior: Nagle's algorithm, delayed ACKs, TCP window scaling, and
proper per-packet delay in both directions. This is the most accurate
simulation short of running on separate physical hosts.

Requires root or `CAP_NET_ADMIN`. The tool sets up the qdisc before
benchmarking and tears it down on exit.

### Fallback: userspace delay injection (`--userspace` flag)

For environments where `sudo` isn't available, the `--userspace` flag
enables an in-process delay model. This wraps each `net.Conn` with a
wrapper that sleeps on direction changes (read→write or write→read).

The userspace model has known limitations:

- **It under-counts SSH round trips.** Go's `crypto/ssh` library
  pipelines multiple SSH messages into single writes, so logically
  separate protocol exchanges (channel-open, exec-request, data) get
  coalesced into fewer direction changes than they would incur as
  separate network round trips.
- **It over-counts HTTPS fresh-connection overhead.** Go's TLS
  implementation does multiple small writes during the handshake that
  each trigger direction-change delays, whereas a real kernel would
  coalesce them into fewer TCP segments.
- **It doesn't model kernel TCP behavior.** Nagle's algorithm, delayed
  ACKs, and TCP window scaling are not captured.

The net effect is that userspace mode compresses the gap between SSH and
HTTPS compared to real networks. The published blog numbers all use
`tc netem`.

### What neither model captures

- **TCP congestion, packet loss, or jitter.** All connections are
  localhost with deterministic delay. Real networks have variance.
- **Real device processing time.** The emulated device responds instantly.
  Real devices have CPU overhead for parsing, AAA lookups, and command
  execution that adds to total latency.
- **TLS session resumption.** The HTTPS fresh-conn benchmark does a full
  TLS 1.3 handshake every time. Real clients may use session tickets to
  reduce subsequent handshakes to 1-RTT or 0-RTT.

### Why the results are still directionally valid

The core finding — that HTTPS requires fewer round trips than SSH for the
same CLI operation — is a property of the protocol design, not the latency
model. SSH's channel-open/exec/data/close sequence requires more
request-response exchanges than HTTPS's single request-response. This
structural difference holds regardless of how latency is injected.

At zero latency (local profile), SSH actually beats HTTPS because the TLS
handshake has higher CPU overhead than the SSH handshake. The HTTPS
advantage only appears when network latency dominates, which is the
scenario the [blog series](https://network-notes.com/posts/2026/cli-over-https-1/) focuses on.

## Sample Results

The `results/` directory contains sample JSON output from benchmark runs.
Files prefixed with `netem-` use `tc netem`; others use the userspace
fallback. The blog series uses `tc netem` results at n=20 for all
published numbers.

## Project Structure

```
cmd/
  clibench/   # Single CLI binary (bench, server, smoketest subcommands)
internal/
  bench/      # Benchmark orchestration (modes, iteration logic)
  device/     # Command engine, prefix matching, transcript loading
  sshserver/  # crypto/ssh server
  httpserver/ # net/http + TLS server (ASA-style API)
  http3server/ # HTTP/3 (QUIC) server (same ASA-style API over QUIC)
  latency/    # Userspace delay injection (fallback, -userspace flag)
  netem/      # tc netem setup via netlink (default, requires root)
  proxy/      # HTTPS→SSH edge proxy (fresh + pooled modes)
  stats/      # Benchmark statistics (percentile, summarize, runParallel)
  tlsutil/    # Shared self-signed TLS config generator
scripts/      # Result generation and helper scripts
transcripts/  # Canned command output files
results/      # Sample benchmark output (JSON)
```

## Development

### Running Tests

```bash
# All tests (no root needed)
go test -race -count=1 ./...

# Netem tests only (requires root / CAP_NET_ADMIN)
sudo go test -race -v -tags netem_root ./internal/netem/
```

### Build Tags

| Tag | Requires | What it gates |
|---|---|---|
| (none) | Nothing | All unit tests, integration tests, proxy tests |
| `netem_root` | Root / `CAP_NET_ADMIN` | Tests that call `netem.Setup()` and verify kernel qdisc state |

### Test Coverage

Every package has unit tests. Integration tests in `internal/` verify
backend equivalence (SSH, HTTPS, HTTP/3, and proxy return identical output for the
same commands), concurrent session handling, and connection pooling modes.
Statistics functions (`internal/stats`) are tested against known values
including sample standard deviation (Bessel's correction).

## License

MIT
