# Architecture

A short overview for maintainers. The code is organised so each concern lives in one package, and the packages can be read on their own.

## Shape

```text
config.yaml ──► src/config   parse, environment overrides, line-aware validation
                   │
        ┌──────────┴───────────┐
        ▼                      ▼
    src/kong               src/loadgen  ◄── src/mock (built-in upstream)
  configure / teardown     run              (also used by src/loadgen for
  kongctl + decK           scheduler        reachability and mode detection)
        │                      │
        ▼                      ▼
   Konnect control plane   Kong data plane ──► upstream (mock or real)
        ▲
        └── src/watch (config file changes) and SIGHUP feed reloads into run
```

`src/main.go` is the Cobra CLI. It loads and validates the configuration, then calls one package per command.

## Packages

| Package | Responsibility | Depends on |
| --- | --- | --- |
| `src/config` | YAML types, `KONNECT_TOKEN` and `KONNECT_REGION` overrides, validation with line numbers, and the user and key naming (`Username`, `APIKey`) shared by every package. | yaml.v3 |
| `src/kong` | Builds the decK state and the kongctl control-plane manifest from the configuration, and runs kongctl and decK. Records a fingerprint of each Konnect part in `generated/applied.json`, so `run` can report drift. | `config`, kongctl and decK binaries |
| `src/mock` | The built-in upstream. Answers for the configured routes, enforces the key on key-auth paths, honours `X-Loadgen-Error` and latency headers, and echoes requests. Also decides whether an upstream URL points at the local mock. | `config` |
| `src/loadgen` | The load generator: scheduler, identity selection, error planning, attribution buckets, statistics, the live console and reload handling. | `config`, `mock` |
| `src/watch` | Reports changes to one file, by watching its directory and debouncing the events. | fsnotify |
| `src/tools/smokeassert` | Checks a `run` summary against its configuration. Used by `make smoke`. | `config`, `loadgen` |

## The load generator

Each endpoint has one scheduler goroutine and one `target`. The target holds its current settings in an atomic pointer, so a reload swaps in new settings without stopping the scheduler.

- **Scheduling** is open loop. Every 5 ms each scheduler adds the credit its current rate earns, and fires one request per unit of credit. Ramp-up scales the rate. Jitter varies the amount of credit one request costs, so the average rate does not change. Responses never delay the scheduler.
- **Concurrency** is a single semaphore of `max_concurrency` slots. A request that finds no free slot is counted as dropped. It is never queued.
- **Identity** is chosen by a selector built from the endpoint's identity. `single-user` returns one identity. `weighted-groups` picks a group by weight and then a user at random. `round-robin` cycles through users with an atomic counter. An endpoint with no identity uses no selector.
- **Errors** are planned per request by `chooseError`, from the endpoint's percentage, mode and whether the upstream is the built-in mock. The planned kind decides the identity, the path, the body and the headers that are sent.
- **Attribution** happens in the collector, once per request, using the identity the request actually carried. The buckets are the group names, `Anonymous` and `Invalid-credentials`.
- **Latency** percentiles come from a reservoir sample of 20,000 responses per endpoint, so memory stays bounded.
- **Reload** runs in its own goroutine. It validates the new configuration, updates settings, starts or stops schedulers for added or removed endpoints, and reports startup-only changes as notices.

## Konnect integration

`configure` runs `kongctl sync` with the manifest in `generated/kongctl.yaml`. kongctl manages the control plane, and its `_deck` block runs `deck gateway sync` for the gateway entities. Every object carries `konnect.tags`, and decK is scoped with `--select-tag kong-loadgen`, so decK never deletes objects it did not create.

The token goes to kongctl through `KONGCTL_DEFAULT_KONNECT_PAT`, and to decK through a 0600 temporary file that is removed afterwards. Output from both tools goes through `redact` before it is printed.

`teardown` runs decK with an empty tagged state to remove the gateway entities, and deletes the control plane only if it carries the `managed-by: kong-loadgen` label.

## Design decisions

- **kongctl by default, decK for gateway entities.** kongctl's control-plane resources do not cover routes, plugins, consumers or keys, so decK does that through `_deck`. Choosing `tool: deck` skips kongctl and runs decK directly against an existing control plane.
- **Identity is real Kong state.** Consumers and key-auth credentials are created in Konnect. The load generator sends the same keys, so Analytics shows the real consumer names.
- **Keys are deterministic.** `key_prefix` plus index, so `run` can rebuild every key from the file, and no secrets store is needed.
- **Attribution by sent credential.** The bucket is what the load generator sent. Konnect may attribute a request differently, for example a 404 with a valid key, and that is expected.
- **No queuing under load.** Open-loop traffic that cannot be sent is dropped and counted. Queuing would hide the backend's slowness and distort the rate.

## Testing

- `make test` runs unit tests, including statistical checks: identity shares, round-robin evenness, the observed error rate against the configured percentage, and bucket attribution through real HTTP against the mock.
- `make smoke` runs 15 seconds against the mock with no Kong, and checks the rate, error rate and bucket shares against the configuration.
- The Konnect path (`configure`, `teardown`) is exercised against a real control plane when a token is available. It is not part of `make test`.

## Extending

- A new config key: add it to `src/config/config.go`, validate it in `validate.go`, then translate it in `src/kong/generate.go` (if it reaches Konnect) or `src/loadgen/run.go` (if it shapes traffic). Document it in `docs/configuration.md`.
- A new error kind: add it to `errorKind` in `src/loadgen/errors.go`, plan it in `chooseError`, build it in `perform`, and add a bucket test.
- A new command: add it in `src/main.go` with a `Long` description, and document it in `docs/usage.md`.
