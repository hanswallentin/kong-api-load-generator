# Usage

Every command reads `config.yaml`, which is beside the binary unless you pass `--config`. Run `kong-loadgen <command> --help` for the same flags below.

```text
Usage:
  kong-loadgen [command]

Available Commands:
  configure   Generate and synchronize tagged Kong resources to Konnect
  run         Generate load according to config.yaml
  teardown    Remove Kong resources managed by this configuration
  upstream    Start the built-in mock upstream
  validate    Validate configuration and report problems with line numbers
  version     Print version information

Flags:
      --config string   path to config.yaml (defaults to the file beside the binary)
  -h, --help            help for kong-loadgen
```

## Global flags

| Flag | Meaning |
| --- | --- |
| `--config <path>` | Read this file instead of `config.yaml` beside the binary. |

## Environment variables

| Variable | Effect |
| --- | --- |
| `KONNECT_TOKEN` | Konnect personal access token. Overrides `konnect.token`. Needed by `validate`, `configure` (unless `--generate-only`) and `teardown`. Never needed by `run` or `upstream`. |
| `KONNECT_REGION` | Overrides `konnect.region` (`us`, `eu`, `au`, `me` or `in`). |
| `NO_COLOR` | When set, the console is not coloured. Colour is off anyway when output is not a terminal. |

## validate

Checks `config.yaml` and reports every problem with its line number. It exits non-zero when there are problems.

```sh
kong-loadgen validate
```

Sample output for a broken file:

```text
line 4: konnect.region must be one of us, eu, au, me, in
line 11: load.client.timeout must be greater than zero
line 21: set exactly one of frequency or frequency_per_minute to a value greater than zero
line 22: error_percentage must be between 0 and 100
line 26: identity.user must name an existing generated user, such as mobile-app-03
line 27: duplicate endpoint name: orders-api
line 28: duplicate endpoint path: /orders
line 29: set exactly one of frequency or frequency_per_minute to a value greater than zero
line 33: auth: none endpoints must not have an identity block
Error: configuration has 9 problem(s)
```

Sample output for a valid file:

```text
Configuration is valid: config.yaml
```

## configure

Generates the Kong configuration and synchronises it to the control plane in `konnect.control_plane`. It prints the planned changes first.

| Flag | Meaning |
| --- | --- |
| `--dry-run` | Show the planned changes and stop. Nothing is changed in Konnect. |
| `--generate-only` | Write the files to `generated/` and stop. Konnect is not contacted, so no token is needed. |

```sh
kong-loadgen configure --dry-run          # preview
kong-loadgen configure --generate-only    # write generated/ only
kong-loadgen configure                    # apply
```

Files written to `generated/`:

| File | Used for |
| --- | --- |
| `kongctl.yaml` | The control plane, plus the `_deck` step that runs decK for gateway entities. |
| `kong.yaml` | The decK state: services, routes, plugins, consumers and API keys, all tagged. |
| `control-plane.yaml` | The control plane alone, used by teardown. |
| `teardown.yaml` | An empty decK state scoped to the tag, used by teardown. |
| `applied.json` | The fingerprint of what was last pushed. `run` reads it to warn about drift. |

`generated/` is git-ignored and safe to inspect or reuse by hand. Its API keys are throwaway demo values.

Sample `--dry-run` output, for a control plane that does not exist yet:

```text
Generated Kong files in generated
Planned Konnect control plane changes:
Plan: 1 to add, 0 to change, 1 external tool step

=== Namespace: default ===
> [2:e:_deck:loadgen-demo] _deck "loadgen-demo" will run external tool steps
  files: [kong.yaml]
  flags: [--select-tag=kong-loadgen]
  depends on: [1:c:control_plane:loadgen-demo]

=== Namespace: kong-loadgen ===
+ [1:c:control_plane:loadgen-demo] control_plane "loadgen-demo" will be created
  name: "loadgen-demo"
  protection: disabled

Gateway changes will be listed after the control plane is created.
```

The gateway part is listed only once the control plane exists. For a control plane that already exists, `--dry-run` also runs `deck gateway diff` and lists every entity it would create, update or delete.

Running `configure` again with nothing changed reports `No changes detected` and does nothing. Removing an endpoint, group or user from `config.yaml` and running `configure` again removes it from Konnect.

### configure --with-apis

Also creates one Konnect API per endpoint, so Konnect Analytics shows API-level traffic alongside the consumer view. Each API is named after its endpoint, with the description and OpenAPI spec from `apis.yaml`, and it is linked to the gateway service that serves the endpoint.

```sh
kong-loadgen apis init                 # write apis.yaml, once
kong-loadgen configure --with-apis     # create the APIs as well
```

| Flag | Meaning |
| --- | --- |
| `--with-apis` | Create the APIs. Without it, `apis.yaml` is not read and no APIs are touched. |
| `--apis-file <path>` | Read this file instead of `apis.yaml` beside `config.yaml`. Giving it implies `--with-apis`, so the file is always used. |

The APIs live in their own kongctl namespace, `kong-loadgen-apis`, so a plain `configure` never deletes them. Removing an endpoint from `apis.yaml` and running `configure --with-apis` removes its API. `--dry-run` and `--generate-only` work as they do for the gateway. The spec for each API is written inline into `generated/kongctl-apis.yaml`.

## apis

### apis init

Writes `apis.yaml` with a generated description (about 50 words) and an OpenAPI 3.0 spec (about 1.5 kB) for every endpoint in `config.yaml`. Each spec contains the endpoint's path and methods.

| Flag | Meaning |
| --- | --- |
| `--force` | Replace an existing `apis.yaml`. Without it, the command refuses to overwrite the file. |
| `--apis-file <path>` | Write to this file instead of `apis.yaml` beside `config.yaml`. |

The file has one entry per endpoint:

```yaml
apis:
  - endpoint: orders-api              # the API is named after the endpoint
    description: Sample API for the orders-api endpoint, used for Konnect Analytics demos. ...
    spec: |
      openapi: 3.0.3
      ...
```

Edit `description` and `spec` freely. The content is only for the API page in Konnect: the traffic is the same whatever the spec says. An endpoint that has no entry in `apis.yaml` gets a generated template at `configure --with-apis` time, with a warning.

## teardown

Removes only what this tool created: the Gateway entities tagged with `konnect.tags`, and the control plane when `create_control_plane` is true and the control plane carries the `managed-by: kong-loadgen` label. A control plane that already existed is never deleted.

| Flag | Meaning |
| --- | --- |
| `--yes` | Delete without the confirmation prompt. Use with care. |

```sh
kong-loadgen teardown          # shows what will be deleted, then asks: Type yes to delete these resources
kong-loadgen teardown --yes    # no prompt
```

`teardown --with-apis` also deletes the Konnect APIs named after the endpoints. It deletes only those that carry the `managed-by: kong-loadgen` label, and refuses to delete an API that another person created with the same name. The APIs are deleted before the gateway entities, and one confirmation covers everything. A plain `teardown` leaves the APIs in place.

Teardown is safe to run twice. When the control plane is already gone it prints this and exits zero:

```text
Control plane "loadgen-demo" does not exist; nothing to tear down.
```

## run

Sends traffic to `gateway.proxy_url` for every endpoint, at the configured rates and error percentages, as the configured users. It never contacts Konnect, so no token is needed.

| Flag | Meaning |
| --- | --- |
| `--duration <d>` | Stop after this long, such as `30s`, `10m` or `1h`. Overrides `load.duration`. Default: run until Ctrl+C. |
| `--json-summary <file>` | Write the final statistics to a JSON file. |
| `--verbose-identity` | Add a per-user table to the live view and the summary. |
| `--seed <n>` | Make the random choices repeatable. `0` (the default) picks a random seed. |

```sh
kong-loadgen run                                         # until Ctrl+C
kong-loadgen run --duration 10m --json-summary run.json  # ten minutes, and keep the numbers
kong-loadgen run --verbose-identity                      # per-user breakdown
```

On a terminal the console redraws every second. Piped output prints only the warnings and the final summary. The live view has this layout. The numbers are illustrative, not from a real run:

```text
kong-loadgen  elapsed 00:00:12  requests 1043  errors 7.9%  dropped 0

ENDPOINT    TARGET  ACTUAL  SUCCESS  ERROR  2XX  4XX  5XX  429  P50   P95   P99
orders-api  100.0   99.8    90.1%    9.9%   1077 57   50   0    31ms  49ms  51ms
catalog     20.0    19.9    100.0%   0.0%   231  0    0    0    28ms  48ms  50ms
account     10.0    10.0    100.0%   0.0%   118  0    0    0    29ms  49ms  50ms

BUCKET               REQUESTS  SHARE  ERRORS
mobile-app           604       57.9%  6.3%
partner-portal       233       22.3%  5.6%
Anonymous            188       18.0%  12.2%
Invalid-credentials  18        1.7%   100.0%
```

Sample summary, printed when the run ends. This run used `--duration 5s --seed 3 --verbose-identity` against the built-in mock. The `ACTUAL` column is an average over the whole run, so it reads lower than `TARGET` when `load.ramp_up` is set.

```text
Run summary (00:00:05)
Requests 518  actual 102.7 rps  errors 7.7%  dropped 0

ENDPOINT    TARGET  ACTUAL  SUCCESS  ERROR  2XX  4XX  5XX  429  P50   P95   P99
orders-api  100.0   78.7    89.9%    10.1%  357  19   21   0    28ms  50ms  51ms
catalog     20.0    15.9    100.0%   0.0%   80   0    0    0    31ms  50ms  51ms
account     10.0    8.1     100.0%   0.0%   41   0    0    0    28ms  50ms  51ms

BUCKET               REQUESTS  SHARE  ERRORS
mobile-app           297       57.3%  6.4%
Anonymous            89        17.2%  10.1%
partner-portal       126       24.3%  4.8%
Invalid-credentials  6         1.2%   100.0%

USER               REQUESTS  SHARE  ERRORS
mobile-app-01      41        7.9%   4.9%
mobile-app-02      23        4.4%   8.7%
...
partner-portal-03  44        8.5%   2.3%
```

Columns:

- **TARGET**: the configured requests per second.
- **ACTUAL**: requests per second. The live view shows the last second, the summary shows the run average.
- **SUCCESS / ERROR**: the share of responses with a status below 400, and the share at 400 or above. A transport failure counts as an error.
- **2XX, 4XX, 5XX, 429**: counts by status class. 429s come from rate limiting.
- **P50, P95, P99**: latency percentiles, from a bounded sample of responses.
- **dropped**: requests skipped because `load.max_concurrency` was full. A non-zero value means the backend is slower than the traffic you asked for.

Ctrl+C stops the run, lets requests in flight finish, and prints the summary. Exit code is zero.

### Live changes

While `run` is going you can change the configuration and see the effect within a few seconds:

- **Save `config.yaml`**. The file is watched.
- **Send SIGHUP**: `kill -HUP <pid>`.

The reload applies changes to `frequency`, `frequency_per_minute`, `error_percentage`, `error_mode`, `identity` (weights, groups, users) and to endpoints that are added or removed. A reload that fails validation is rejected, and the previous settings keep running:

```text
15:04:12 warning: config reload rejected, keeping the previous settings: config.yaml: line 22: error_percentage must be between 0 and 100
```

A successful reload prints a summary line:

```text
15:04:13 config reloaded: 3 endpoint(s) updated, 0 added, 0 removed
```

Some settings can only change on restart. A change to them is reported, and the old value stays in effect until you restart:

```text
15:04:20 warning: gateway.proxy_url changed; restart run to apply it
```

Those settings are `gateway.proxy_url`, `load.max_concurrency`, `load.ramp_up`, `load.jitter`, `load.duration` and `load.client.timeout`.

A change that Konnect needs (a route, plugin or consumer) takes effect in the traffic at once, but the gateway keeps the old configuration until you run `configure`. `run` warns about that:

```text
15:04:13 warning: changed since the last configure: consumers and API keys; run: kong-loadgen configure (traffic for these still goes to the old Konnect config)
```

## upstream

Starts the built-in mock backend on the port in `upstream.default_url`. The mock answers only for the configured paths, so it behaves like a real backend in front of Kong:

- an unknown path returns 404
- a key-auth path without a valid key returns 401
- a malformed JSON body returns 400
- `X-Loadgen-Error: <code>` returns that status
- it sleeps for the latency the load generator asks for
- it returns 200 with a JSON body that echoes the request, including the consumer headers Kong adds

| Flag | Meaning |
| --- | --- |
| `--listen <addr>` | Address to listen on. Default: the port from `upstream.default_url`, on all interfaces. |

```sh
kong-loadgen upstream
```

```text
Mock upstream listening on [::]:9000 with 3 endpoint(s). Press Ctrl+C to stop.
```

The mock listens on all interfaces so a Kong data plane in Docker can reach it through `host.docker.internal`. It echoes headers, so do not expose it to untrusted networks.

## version

```sh
kong-loadgen version
```

Prints the version. Release builds set it with `make build VERSION=<version>`. Local builds print `dev`.

## Make targets

| Target | What it does |
| --- | --- |
| `make build` | Builds `./kong-loadgen` for this machine. |
| `make test` | Runs the unit tests. |
| `make smoke` | Builds, starts the mock, sends 15 seconds of traffic straight at it (no Kong), and checks the rates, error rates and bucket shares. |
| `make dist` | Packages darwin and linux binaries for amd64 and arm64 into `dist/`, each with `config.yaml`, `README.md` and `docs/`. |
| `make clean` | Removes the binary and `dist/`. |
