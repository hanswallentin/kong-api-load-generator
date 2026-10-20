# Configuration reference

`config.yaml` is the single source of truth. `configure` turns it into Kong resources, and `run` turns it into traffic. The sample file in the repository is commented, and this page lists every key.

Conventions used below:

- **Required** means validation fails without it.
- **Default** is what applies when the key is omitted.
- Durations are Go-style strings such as `30s`, `10m` or `1h`, or a whole number of seconds.
- Validation errors include the line number, so `kong-loadgen validate` points you to the problem.

## konnect

Where `configure` and `teardown` push the configuration.

| Key | Type | Default | Allowed values | Notes |
| --- | --- | --- | --- | --- |
| `tool` | string | required | `kongctl`, `deck` | `kongctl` manages the control plane and runs decK for gateway entities. `deck` manages gateway entities only. |
| `token` | string | required unless `KONNECT_TOKEN` is set | any personal access token | Leave it empty and export `KONNECT_TOKEN`. The environment variable always wins, and the token is never written to files or output. |
| `region` | string | required | `us`, `eu`, `au`, `me`, `in` | Selects the Konnect API URL, such as `https://eu.api.konghq.com`. Overridden by `KONNECT_REGION`. |
| `control_plane` | string | required | a control plane name | The control plane the configuration is pushed to. |
| `create_control_plane` | bool | `false` | `true`, `false` | `true` creates the control plane if it is missing, and labels it `managed-by: kong-loadgen` so teardown can delete it. `false` requires the control plane to exist. `deck` requires `false`. |
| `tags` | list of strings | `[]` | any strings | Added to every Konnect object, so teardown finds them. `kong-loadgen` is always added as well. |

```yaml
konnect:
  tool: kongctl
  token: ""                  # export KONNECT_TOKEN instead
  region: eu
  control_plane: loadgen-demo
  create_control_plane: true
  tags: [kong-loadgen, sales-demo]
```

## gateway

Where the load goes.

| Key | Type | Default | Allowed values | Notes |
| --- | --- | --- | --- | --- |
| `proxy_url` | URL | required | an absolute URL, such as `http://localhost:8000` | The Kong data plane. Every endpoint's path is appended to it. |
| `host_header` | string | `null` | any host name, or `null` | When set, the `Host` header on every request is replaced. Useful when routing depends on host name. |

```yaml
gateway:
  proxy_url: http://localhost:8000
  host_header: null
```

## upstream

Where Kong forwards the traffic.

| Key | Type | Default | Allowed values | Notes |
| --- | --- | --- | --- | --- |
| `default_url` | URL | required | an absolute URL with an explicit port | Used for every endpoint that has no `upstream_url`. For `kong-loadgen upstream`, the port is the one the mock listens on. |

When the upstream points at this machine (`localhost`, `127.0.0.1`, `::1` or `host.docker.internal`), it is treated as the built-in mock. That is what makes `error_mode: upstream` work. Any other host is a real service, and error generation falls back to client errors.

| Where Kong runs | `default_url` |
| --- | --- |
| In Docker, on the same machine as the mock | `http://host.docker.internal:9000` |
| On the host | `http://localhost:9000` |

Inside a container, `localhost` refers to the container, so `http://localhost:9000` fails there. This is the most common cause of a `502` from the gateway. See [troubleshooting](troubleshooting.md).

## load

How hard and how fast the traffic goes.

| Key | Type | Default | Allowed values | Notes |
| --- | --- | --- | --- | --- |
| `duration` | duration | `0` | `0` (until Ctrl+C) or a duration | Overridden by `run --duration`. |
| `ramp_up` | duration | `0` (no ramp) | a duration | Rates rise linearly from near zero to their configured values over this period. |
| `max_concurrency` | int | required | `1` to any number | The most requests in flight at once, across all endpoints. When the limit is reached, scheduled requests are counted as `dropped` rather than queued. |
| `jitter` | float | `0` | `0` to `1` | Each gap between requests varies by up to this fraction either way. `0.2` means plus or minus 20%, so graphs look organic. The average rate is unchanged. |
| `error_statuses` | list of ints | `[500, 502, 503]` | HTTP error codes `400` to `599` | The statuses used for upstream-generated errors (`X-Loadgen-Error`). |
| `client.timeout` | duration | required | a duration above zero | How long to wait for a response before counting a transport error. |

```yaml
load:
  duration: 0
  ramp_up: 30s
  max_concurrency: 200
  jitter: 0.2
  error_statuses: [500, 502, 503]
  client:
    timeout: 10s
```

`ramp_up`, `jitter`, `duration`, `max_concurrency` and `client.timeout` apply when `run` starts. Changing them while `run` is going is reported, and takes effect after a restart.

## users

The simulated people. Each one becomes a Kong consumer with a key-auth credential.

| Key | Type | Default | Allowed values | Notes |
| --- | --- | --- | --- | --- |
| `key_header` | string | `apikey` | a valid HTTP header name | The header that carries the API key. The key-auth plugin on each route reads this same header. |
| `groups[].name` | string | required | non-empty, unique across groups | The group name. It is the bucket name in the statistics and the consumer tag in Konnect. |
| `groups[].count` | int | required | above `0` | How many users the group contains. |
| `groups[].key_prefix` | string | required | unique across groups | The prefix of the API keys. |

Each group creates users named `<name>-NN` and keys `<key_prefix>-NN`, where `NN` runs from `01` to `count`.

Each group also becomes a Konnect consumer group, named after the group, that holds all of the group's users. Use it to scope plugins to a whole group in Konnect, or to find a group's users at a glance.

```yaml
users:
  key_header: apikey
  groups:
    - name: mobile-app       # mobile-app-01 .. mobile-app-10
      count: 10
      key_prefix: mobile     # keys mobile-01 .. mobile-10
    - name: partner-portal   # partner-portal-01 .. partner-portal-03
      count: 3
      key_prefix: partner    # keys partner-01 .. partner-03
```

The keys are deterministic, so `run` can rebuild them from the configuration, and there is no secrets store. They are throwaway demo values. They appear in the generated files and the console output, and must never be real credentials.

Changing `count` or `key_prefix` does not take effect in Konnect until you run `configure` again. `run` warns about this.

## endpoints

Each endpoint is both a Kong service and route, and a load target. The list can be empty only if there are no endpoints, which validation rejects.

| Key | Type | Default | Allowed values | Notes |
| --- | --- | --- | --- | --- |
| `name` | string | required | unique | The service name in Konnect, and the endpoint name in the statistics. |
| `path` | string | required | starts with `/`, unique | The route path, and the path the load is sent to. |
| `methods` | list | `[GET]` | HTTP methods | The methods the route accepts. Each request picks one of them at random. |
| `upstream_url` | URL or `null` | `null` | an absolute URL | Overrides `upstream.default_url` for this endpoint. |
| `frequency` | float | one of the two | above `0` | Requests per second. |
| `frequency_per_minute` | float | one of the two | above `0` | Requests per minute. Set exactly one of `frequency` and `frequency_per_minute`. |
| `error_percentage` | float | `0` | `0` to `100` | The share of requests that should fail. See [error generation](#error-generation). |
| `error_mode` | string | `mixed` | `upstream`, `client`, `mixed` | Which kinds of error to generate. |
| `latency_ms.min` | int | `0` | `0` or more, not above `max` | The mock upstream waits a random time in this range. |
| `latency_ms.max` | int | `0` | `min` or more | |
| `auth` | string | required | `none`, `key-auth` | `key-auth` enables the key-auth plugin on the route. |
| `identity` | block | required with `key-auth`, forbidden with `none` | see below | Which users send this endpoint's traffic. |
| `headers` | map | `{}` | header name to value | Sent with every request. |
| `body` | string | none | any string | Sent as the request body with `Content-Type: application/json`. |
| `plugins` | list | `[]` | see below | Plugins to enable on this endpoint's route. |

```yaml
endpoints:
  - name: orders-api
    path: /orders
    methods: [GET, POST]
    frequency: 20
    error_percentage: 10
    error_mode: mixed
    latency_ms: { min: 20, max: 300 }
    auth: key-auth
    identity:
      mode: weighted-groups
      groups:
        - { group: mobile-app, weight: 70 }
        - { group: partner-portal, weight: 30 }
    headers: { X-Demo: "true" }
    body: '{"item":"book"}'
    plugins:
      - name: rate-limiting
        config: { minute: 600 }
```

Rates are per endpoint. The total load is the sum of the endpoint rates.

### identity

Required with `auth: key-auth`. Not allowed with `auth: none`.

| Key | Type | Used by | Notes |
| --- | --- | --- | --- |
| `mode` | string | all | `single-user`, `weighted-groups` or `round-robin`. |
| `user` | string | `single-user` | A generated user, such as `mobile-app-03`. It must exist. |
| `groups` | list | `weighted-groups`, `round-robin` | Each item has `group` (an existing group, listed once) and, for `weighted-groups` only, `weight`. |
| `groups[].weight` | int | `weighted-groups` | Each weight must be above zero. The weights are normalised, so `7` and `3` give the same split as `70` and `30`. They do not need to add up to 100. |

Modes:

- **`single-user`**: every request is sent as the named user. Use it to show one person's traffic.
- **`weighted-groups`**: each request picks a group at random in proportion to its weight, then a user in that group at random. Use it to control the share of traffic per group.
- **`round-robin`**: the requests cycle through every user in the listed groups, in order. The traffic is perfectly even per user.

```yaml
identity: { mode: single-user, user: mobile-app-03 }
```

### auth: none

No identity and no key-auth plugin. Every request is counted in the `Anonymous` bucket.

```yaml
- name: public-catalog
  path: /catalog
  methods: [GET]
  frequency: 5
  error_percentage: 0
  auth: none
```

### plugins

Each item is passed through to Kong on this endpoint's route.

| Key | Type | Notes |
| --- | --- | --- |
| `name` | string | A Kong plugin name, such as `rate-limiting`. |
| `config` | map | The plugin configuration, in the plugin's own schema. |

```yaml
plugins:
  - name: rate-limiting
    config: { minute: 60 }
```

`key-auth` is added automatically for `auth: key-auth`. Do not list it under `plugins`.

## Error generation

`error_percentage` is the share of requests on an endpoint that should end in an error. Each request that is chosen to fail gets one of these outcomes:

| Outcome | How it is produced | Status | Bucket |
| --- | --- | --- | --- |
| `upstream` | The request carries `X-Loadgen-Error: <code>`, and the mock returns that code. Codes come from `load.error_statuses`. | 500, 502 or 503 by default | the user's group |
| missing key | No key is sent to a `key-auth` endpoint. | 401 from Kong | `Anonymous` |
| wrong key | A key Kong does not know is sent. | 401 from Kong | `Invalid-credentials` |
| unknown path | A path no route matches is requested. | 404 from Kong | `Anonymous` |
| malformed body | Invalid JSON is sent with `Content-Type: application/json`. | 400 from the upstream | the user's group |

`error_mode` chooses which outcomes are used:

- `upstream`: only the `X-Loadgen-Error` errors. Requires the built-in mock. Otherwise falls back to `client` with a warning.
- `client`: only the client-side errors. Works with any upstream. Endpoints with `auth: none` get only unknown path and malformed body, because they have no keys to omit or get wrong.
- `mixed` (the default): each failing request is an `upstream` error with probability one half, and otherwise a client error, chosen at random from the ones the endpoint supports.

The observed error rate can be higher than `error_percentage`, because it also counts real errors such as `429` from rate limiting. The configured percentage covers only the errors this tool generates on purpose.

## Attribution buckets

Every request is counted in exactly one bucket. The bucket is decided by the credential the load generator sent, not by what Konnect reports.

| Bucket | What it contains |
| --- | --- |
| A group name, such as `mobile-app` | Requests sent with a valid generated key. |
| `Anonymous` | Requests with no credentials. This includes all traffic to `auth: none` endpoints, requests with no key to a `key-auth` endpoint, and requests for unknown paths. |
| `Invalid-credentials` | Requests sent with a key that Kong does not know. |

The live view and the summary show these as rows next to the groups. With `--verbose-identity`, every user gets a row too.

In Konnect Analytics, requests from `Anonymous` and `Invalid-credentials` are expected to have no consumer, so they appear as a separate, consumer-less group. Verify this on your Konnect release before the demo.

## Finding consumers and keys in Konnect

Each user is a Kong consumer named like `mobile-app-03`, tagged `kong-loadgen` and with its group name (`mobile-app`), and it has a key-auth credential with the key `mobile-03`.

To look one up in the Konnect UI, open the control plane, go to the consumers, and search for `mobile-app-03`. Open it to see its key-auth credential. Menu labels change between Konnect releases, so check them in your release. The same data is available from the command line:

```sh
deck gateway dump --konnect-addr https://eu.api.konghq.com \
  --konnect-control-plane-name loadgen-demo \
  --konnect-token-file <file-with-token> --select-tag kong-loadgen -o dump.yaml
```

To see traffic per consumer, open Konnect Analytics, go to the Explorer, and group by **Consumer**. The Explorer groups and filters by consumer, route, status code and gateway service, according to [Konnect's Explorer docs](https://developer.konghq.com/advanced-analytics/explorer/).

## Environment variables

| Variable | Overrides | Notes |
| --- | --- | --- |
| `KONNECT_TOKEN` | `konnect.token` | Needed for `validate`, `configure` and `teardown`. Not needed for `run` or `upstream`. |
| `KONNECT_REGION` | `konnect.region` | Must be one of the allowed regions. |
| `NO_COLOR` | colour | Turns colour off when set, even on a terminal. |

## Worked example: a complete demo

The repository's `config.yaml` is a complete example. It has three endpoints:

1. `public-catalog`: anonymous, no errors, shows the `Anonymous` bucket.
2. `orders-api`: key-auth, 10% errors, mobile and partner groups weighted 70/30.
3. `checkout-api`: key-auth, round-robin over every user, rate-limited to 60 calls per minute per consumer.

Change the weights, the error percentage or the rates, save the file, and `run` applies them within a few seconds.
