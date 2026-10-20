# kong-loadgen maintainer notes

## Commands

- `make build` builds the current-platform binary.
- `make test` runs unit tests.
- `make smoke` runs the 15-second local smoke test.
- `make dist` packages darwin/linux binaries for amd64/arm64.
- `go run ./src validate --config config.yaml` validates configuration.

## Architecture

The Cobra CLI is in `src/main.go`. YAML types and line-aware validation are in `src/config/`. Kong translation, mock upstream, and load execution are isolated behind command packages as those phases are implemented. Configuration is the source of truth for both Kong resources and traffic targets.

## Configuration schema

The sample and canonical schema are in the root `config.yaml`; parser types live in `src/config/`. User-facing details belong in `docs/configuration.md`.

## Conventions

- Keep configuration-driven behavior deterministic where practical, including generated demo credentials.
- Use API-key authentication only; do not add password or basic-auth flows.
- Keep validation errors actionable and include YAML line numbers.
- Generated demo API keys are throwaway values; never use real secrets in the sample configuration.
- **Never log or commit the Konnect token.** Prefer `KONNECT_TOKEN` over a config-file token and mask secrets in output.