#!/bin/sh
# Starts the built-in mock upstream, drives 15 seconds of load straight at it
# (no Kong needed), and checks the result against src/testdata/smoke.yaml.
set -eu

cd "$(dirname "$0")/.."
config=src/testdata/smoke.yaml
work=$(mktemp -d)
upstream_pid=""

cleanup() {
	if [ -n "$upstream_pid" ]; then
		kill "$upstream_pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

./kong-loadgen upstream --config "$config" >"$work/upstream.log" 2>&1 &
upstream_pid=$!

attempts=0
until curl -s -o /dev/null http://localhost:9000/; do
	attempts=$((attempts + 1))
	if [ "$attempts" -gt 50 ]; then
		cat "$work/upstream.log"
		echo "smoke: mock upstream did not start on localhost:9000" >&2
		exit 1
	fi
	sleep 0.1
done

./kong-loadgen run --config "$config" --duration 15s --seed 42 --json-summary "$work/summary.json"
go run ./src/tools/smokeassert --config "$config" --summary "$work/summary.json"
