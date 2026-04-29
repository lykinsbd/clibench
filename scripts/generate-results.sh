#!/usr/bin/env bash
set -euo pipefail

# Generate all benchmark results for clibench.
# Netem runs require sudo / CAP_NET_ADMIN.
# Usage:
#   ./scripts/generate-results.sh          # all runs (netem + userspace)
#   ./scripts/generate-results.sh netem    # netem only (requires sudo)
#   ./scripts/generate-results.sh userspace # userspace only (no sudo)

BENCH="./bin/bench"
RESULTS="results"
ITERATIONS=20
MODE="${1:-all}"

if [[ ! -x "$BENCH" ]]; then
    echo "Building bench binary..."
    go build -o "$BENCH" ./cmd/bench
fi

mkdir -p "$RESULTS"

run_netem() {
    echo "=== Netem runs (requires sudo) ==="
    for profile in local campus regional continental intercontinental; do
        echo "  netem $profile 5cmd..."
        sudo "$BENCH" -latency "$profile" -iterations "$ITERATIONS" -commands 5 \
            > "$RESULTS/netem-${profile}-${ITERATIONS}iter-5cmd.json"
    done

    echo "  netem regional scaling..."
    for cmds in 1 10 25 50; do
        echo "  netem regional ${cmds}cmd..."
        sudo "$BENCH" -latency regional -iterations "$ITERATIONS" -commands "$cmds" \
            > "$RESULTS/netem-regional-${ITERATIONS}iter-${cmds}cmd.json"
    done
}

run_userspace() {
    echo "=== Userspace runs ==="
    for profile in local campus regional continental intercontinental; do
        echo "  userspace $profile 5cmd..."
        "$BENCH" -latency "$profile" -iterations "$ITERATIONS" -commands 5 -userspace \
            > "$RESULTS/${profile}-${ITERATIONS}iter-5cmd.json"
    done

    echo "  userspace regional scaling..."
    for cmds in 1 10 25 50; do
        echo "  userspace regional ${cmds}cmd..."
        "$BENCH" -latency regional -iterations "$ITERATIONS" -commands "$cmds" -userspace \
            > "$RESULTS/regional-${ITERATIONS}iter-${cmds}cmd.json"
    done
}

case "$MODE" in
    all)
        rm -f "$RESULTS"/*.json
        run_netem
        run_userspace
        ;;
    netem)
        rm -f "$RESULTS"/netem-*.json
        run_netem
        ;;
    userspace)
        rm -f "$RESULTS"/[!n]*.json "$RESULTS"/n[!e]*.json 2>/dev/null || true
        run_userspace
        ;;
    *)
        echo "Usage: $0 [all|netem|userspace]" >&2
        exit 1
        ;;
esac

echo "=== Done. Results in $RESULTS/ ==="
ls -1 "$RESULTS"/*.json
