# Benchmark Results

Sample benchmark output from `clibench`. Each versioned directory contains
results generated with that release's binary.

## Directory Structure

```
results/
  v0.5.0/
    netem-local-n20-5cmd.json
    netem-campus-n20-5cmd.json
    netem-regional-n20-5cmd.json
    netem-continental-n20-5cmd.json
    netem-intercontinental-n20-5cmd.json
    netem-transpacific-n20-5cmd.json
  v0.6.0/
    netem-local-n20-5cmd.json
    netem-campus-n20-5cmd.json
    netem-regional-n20-5cmd.json
    netem-leo-n20-5cmd.json
    netem-continental-n20-5cmd.json
    netem-leo-remote-n20-5cmd.json
    netem-intercontinental-n20-5cmd.json
    netem-transpacific-n20-5cmd.json
    netem-geo-n20-5cmd.json
```

## Naming Convention

```
netem-{profile}-n{iterations}-{commands}cmd.json
```

- **netem** — uses `tc netem` kernel latency injection (requires root)
- **profile** — latency profile name (local, campus, regional, etc.)
- **n{iterations}** — number of iterations per benchmark mode
- **{commands}cmd** — commands executed per iteration

## Reproducing

```bash
go build -o bin/clibench ./cmd/clibench/
sudo ./bin/clibench bench \
  --latency regional \
  --iterations 20 \
  --commands 5 \
  --output json > results/v0.5.0/netem-regional-n20-5cmd.json
```

Each file contains all transports and modes (21 modes in v0.5.0) from a
single benchmark run. The blog series uses `--iterations 20 --commands 5`
as the standard configuration.

## What's in Each Result

Every JSON entry includes:

| Field | Description |
|---|---|
| `transport` | ssh, https, http3, proxy, tunnel |
| `operation` | Mode name (fresh-conn, keep-alive, etc.) |
| `round_trips` | Median write→read direction changes |
| `read_ops` | Median application-level Read() calls |
| `write_ops` | Median application-level Write() calls |
| `packets_in` | Real wire packets inbound (AF_PACKET) |
| `packets_out` | Real wire packets outbound (AF_PACKET) |
| `avg_ms` | Mean latency |
| `p50_ms` | Median (50th percentile) latency |
| `p95_ms` | 95th percentile latency |

## Blog References

The [CLI Over HTTPS](https://network-notes.com/posts/2026/cli-over-https-1/)
blog series uses results from the `regional` profile (30ms RTT) for all
published comparisons.
