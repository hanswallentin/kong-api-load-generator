# kong-loadgen maintainer notes

## Commands

- `make build` builds the current-platform binary (`./kong-loadgen`).
- `make test` runs unit tests, including statistical checks (identity shares, error rates).
- `make smoke` builds, starts the built-in mock, drives 15 seconds of load straight at it (no Kong), and checks the result with `src/tools/smokeassert`. Uses `src/testdata/smoke.yaml`.
- `make dist` packages darwin/linux binaries for amd64/arm64 as tarballs (binary, `config.yaml`, `README.md`, `docs/`).
- `go run ./src validate --config config.yaml` validates configuration.
- `go test -race ./src/loadgen/ ./src/mock/` checks the concurrent code.

## Architecture

- `src/main.go`: Cobra CLI (`validate`, `configure`, `teardown`, `run`, `upstream`, `version`). Reads config, applies environment overrides, and calls the packages below.
- `src/config/`: YAML types, environment overrides (`KONNECT_TOKEN`, `KONNECT_REGION`), line-aware validation, and the shared naming of users and keys (`UserGroup.Username`, `UserGroup.APIKey`).
- `src/kong/`: translates config into decK state, a kongctl control-plane manifest, and teardown files. `deploy.go` runs kongctl and decK with the token passed via env var or a 0600 temp file.
- `src/mock/`: the built-in upstream. Returns 404/405/401/400 based on the config, honours `X-Loadgen-Error` and the latency headers, and echoes request headers.
- `src/loadgen/`: the load generator. An open-loop scheduler per endpoint (credit-based, so ramp-up and jitter don't drift the rate), a global concurrency semaphore that drops rather than queues, identity selection, error planning, attribution buckets, reservoir-sampled latency, and the live console.
- `docs/architecture.md` has the longer overview.

## Configuration schema

The sample and canonical schema are in the root `config.yaml`; parser types live in `src/config/`. User-facing details belong in `docs/configuration.md`.

## Conventions

- Never log or commit the Konnect token. Prefer `KONNECT_TOKEN` over a config-file token. Mask secrets in all output (`redact` in `src/kong/deploy.go`).
- Keep configuration-driven behaviour deterministic where practical: generated demo keys are `key_prefix` plus index, and `--seed` makes runs repeatable.
- Use API-key authentication only; do not add password or basic-auth flows.
- Keep validation errors actionable and include YAML line numbers.
- Generated demo API keys are throwaway values; never use real secrets in the sample configuration.
- Commands that never contact Konnect (`run`, `upstream`) do not require a token. `validate`, `configure` and `teardown` do.
- `teardown` deletes only entities tagged `konnect.tags`, and deletes the control plane only if it carries the `managed-by: kong-loadgen` label.
