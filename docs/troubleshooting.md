# Troubleshooting

Each entry gives the symptom, the usual cause, and the fix. Error text is quoted exactly as the tool prints it, so you can search for it.

## Tools and setup

### `required Kong tool "kongctl" was not found on PATH`

**Cause.** `configure` or `teardown` needs kongctl and decK, and at least one is missing. kongctl runs decK for the gateway entities, so both must be installed.

**Fix.** Install both, then open a new terminal so PATH is up to date:

```sh
brew install kong/kongctl/kongctl
brew install kong/deck/deck
```

Check with `kongctl version` and `deck version`. The message names the install page for the tool that is missing.

`validate`, `configure --generate-only`, `run` and `upstream` do not need either tool.

### `Konnect token is required; set KONNECT_TOKEN or konnect.token`

**Cause.** `validate`, `configure` and `teardown` need a token, and neither the environment nor the file provides one.

**Fix.** Export it in the same shell you run the tool from:

```sh
export KONNECT_TOKEN=<your personal access token>
```

Check that it is set without printing it: `[ -n "$KONNECT_TOKEN" ] && echo set`. A new terminal does not inherit a variable that was exported in another one.

### Token or region mismatch

**Symptom.** kongctl or decK fails with an authentication or authorisation error, or `configure --dry-run` cannot find a control plane that you can see in the Konnect UI.

**Cause.** The token was created in one Konnect region, and `konnect.region` or `KONNECT_REGION` points at another. Each region has its own API host, such as `https://eu.api.konghq.com`.

**Fix.** Set `konnect.region` (or `KONNECT_REGION`) to the region where your organisation lives. Create the token in the same organisation. Then test the token with:

```sh
KONGCTL_DEFAULT_KONNECT_PAT="$KONNECT_TOKEN" kongctl get gateway control-planes --region eu -o json
```

If that lists your control planes, the token and region match.

Tokens can also expire. Create a new one and export it again.

### `control plane "<name>" does not exist` or no control plane found

**Cause.** With `create_control_plane: false`, the control plane named in `konnect.control_plane` must already exist. The name is case-sensitive and must match exactly.

**Fix.** Either create the control plane in Konnect with that exact name, or set `create_control_plane: true` so `configure` creates it. For decK (`tool: deck`) it must already exist.

`teardown` on a control plane that is already gone prints `does not exist; nothing to tear down`. That is not an error.

`configure` also fails when more than one control plane has the same name. Its message is `found N control planes named ...; rename one before continuing`. Rename one in Konnect.

### `refusing to delete it` (teardown)

**Cause.** `teardown` deletes the control plane only when this tool created it, and it checks the `managed-by: kong-loadgen` label. A control plane that already existed, or was created by hand, is left alone.

**Fix.** This is the protection working. To remove a hand-made control plane, delete it in Konnect yourself.

### `listen on :9000: ... address already in use` (or "is another process using the port?")

**Cause.** The port in `upstream.default_url` is already used, often by a previous `kong-loadgen upstream` that is still running, or by another service.

**Fix.** Find the process and stop it:

```sh
lsof -nP -iTCP:9000 -sTCP:LISTEN
```

Or start the mock on another port by changing `upstream.default_url`, and update the Kong service to match with `configure`. `--listen` overrides the address for one run.

## Gateway and data plane

### The data plane is not connected

**Symptom.** Konnect shows no data plane for the control plane, or the control plane's data-plane list is empty. Requests to `gateway.proxy_url` are refused, or Kong returns `404` for every path.

**Cause.** The data plane is attached to a different control plane from `konnect.control_plane`, or it is not running. A data plane only serves the configuration of the control plane it is attached to.

**Fix.**

1. Find the control plane your data plane is attached to. For a Docker data plane, the `KONG_CLUSTER_CONTROL_PLANE` variable names its endpoint. Match the endpoint to the control plane in Konnect.
2. Either set `konnect.control_plane` to that control plane, with `create_control_plane: false`, and run `configure`. Or attach a data plane to the control plane you configured.

`configure` writes only tagged objects into a control plane that already exists. Check `--dry-run` first to see exactly what it would add.

### `502 Bad Gateway` from the gateway

**Cause.** Kong could not reach the upstream. The most common reason is `upstream.default_url` set to `http://localhost:9000` while Kong runs in Docker. Inside the container, `localhost` is the container, not your machine. Another cause is that the mock is not running.

**Fix.**

1. Start the mock: `./kong-loadgen upstream`.
2. For a Docker data plane, set `upstream.default_url: http://host.docker.internal:9000`, then run `configure` again. `configure` updates the service URL in Konnect.
3. Check that the URL's port matches the mock's port.

`run` warns when the built-in mock is configured but not reachable:

```text
warning: upstream http://host.docker.internal:9000 is not reachable; start the built-in mock with: kong-loadgen upstream
```

### `401` from the gateway

**Causes.**

- The request had no key, or a key Kong does not know. In the console these are the `Anonymous` and `Invalid-credentials` buckets, which are expected on purpose if the error percentage is above zero.
- If every request on a key-auth endpoint is 401, the key header in the request does not match the header the plugin reads. See the next entry.
- The consumer or key was not pushed. Run `configure`, and check that `run` does not warn about drift.

**Check.** On a key-auth endpoint, the console should show 401s only in the `Anonymous` and `Invalid-credentials` rows, with a share close to `error_percentage`. If valid-user rows show 401s, the header is wrong.

### Key header mismatch

**Symptom.** Every request to a key-auth endpoint is 401, even though the keys are correct.

**Cause.** `users.key_header` and the key-auth plugin's `key_names` differ. The tool sets both from `users.key_header`, so a mismatch means the configuration was changed without running `configure`, or the plugin was edited by hand in Konnect.

**Fix.** Make `users.key_header` the header you send, then run `configure`. `configure --generate-only` writes `key_names` to `generated/kong.yaml`, so you can check it without contacting Konnect:

```sh
./kong-loadgen configure --generate-only
grep -A3 "key_names" generated/kong.yaml
```

### `403 Forbidden` from the gateway

**Cause.** Kong refused the request on purpose, for example through an access-control plugin you added to an endpoint in Konnect, not through this tool. The tool does not configure any plugin that returns 403.

**Fix.** Check the plugins on the route in Konnect. Remove the plugin or allow the request.

### `404` from the gateway

**Causes.**

- The route does not exist in the data plane. The configuration was not pushed, or the data plane is attached to another control plane. Run `configure`, then `configure --dry-run` should show no changes.
- The path is different. The endpoint's `path` and the route's path must match. Remember that `unknown path` errors are meant to return 404, so some 404s are expected when `error_percentage` is above zero.

**Check.** Each request to an existing route returns 200 from the mock with a JSON body. Kong's own 404 for a path that no route matches usually says `no Route matched with those values`. Check the body to tell it apart from a 404 from the mock.

### `429 Too Many Requests`

**Cause.** A rate-limiting plugin is limiting requests, usually per consumer. This is expected on `checkout-api` with `minute: 60`, and it shows in the 429 column. The limit is per consumer, so a round-robin endpoint can be limited for some users and not others.

**Fix.** Raise the limit in `config.yaml` and run `configure`, or lower the frequency. Nothing needs fixing if you wanted the demo to show rate limiting.

### Configuration changes are not visible in Konnect

**Symptom.** You changed a route, plugin, consumer or key, and Konnect still shows the old value. Or `run` prints a drift warning.

**Cause.** Routes, plugins, consumers and keys reach Konnect only through `configure`. Rates, error percentages and identities take effect in `run` immediately, but the gateway keeps the old configuration until you push.

**Fix.** Run `./kong-loadgen configure --dry-run` to see the pending change, then `./kong-loadgen configure`. The warning names the part that changed: services and routes, plugins, consumers and API keys, or the control plane.

## Configuration

### `endpoint ... key-auth endpoints require an identity block`

**Cause.** An endpoint has `auth: key-auth` but no `identity:` block. Every key-auth endpoint needs one, so the tool knows whose requests to send.

**Fix.** Add an identity block, or change `auth` to `none` and remove the identity block. The error names the line of the endpoint.

### `auth: none endpoints must not have an identity block`

**Cause.** The reverse: an endpoint has `auth: none`, and an `identity:` block. Anonymous endpoints have no identity.

**Fix.** Remove the identity block, or change `auth` to `key-auth`.

### `identity.user must name an existing generated user, such as mobile-app-03`

**Cause.** `single-user` mode names a user that the groups do not generate, for example `mobile-app-12` when `mobile-app` has `count: 10`.

**Fix.** Use a name in the form `<group>-NN`, where `NN` is within the group's `count`.

### `identity references unknown user group: <name>`

**Cause.** An identity lists a group name that is not in `users.groups`. Names are case-sensitive.

**Fix.** Correct the name, or add the group.

### `set exactly one of frequency or frequency_per_minute to a value greater than zero`

**Cause.** The endpoint sets both fields, neither, or a value of zero.

**Fix.** Keep one of the two, with a positive value.

### `duplicate endpoint name` or `duplicate endpoint path`

**Cause.** Two endpoints share a name, or a path. Kong routes need unique paths.

**Fix.** Rename one of them.

### `user group key_prefix values must be unique`

**Cause.** Two groups use the same `key_prefix`. Their keys would collide.

**Fix.** Give each group its own prefix.

### Konnect and `config.yaml` disagree about `frequency` or `error_percentage`

These are not sent to Konnect, so they never appear there. Only `run` uses them. Check the console, not Konnect, for the rate and error settings.

## Running traffic

### `configure has not been run for this configuration`

**Cause.** `run` looks for the record that `configure` leaves in `generated/applied.json`. There is none, so the tool cannot tell what Konnect has.

**Fix.** Run `./kong-loadgen configure`. Teardown removes the record, so this warning comes back after a teardown.

### `config reload rejected, keeping the previous settings`

**Cause.** You saved a `config.yaml` that does not validate. The message gives the line number. The previous settings keep running.

**Fix.** Fix the line, and save again. The next save is applied.

### `restart run to apply it`

**Cause.** You changed a setting that cannot change during a run: `gateway.proxy_url`, `load.max_concurrency`, `load.ramp_up`, `load.jitter`, `load.duration` or `load.client.timeout`.

**Fix.** Press Ctrl+C, and start `run` again.

### `dropped` is above zero

**Cause.** `load.max_concurrency` is full. Requests that would have waited are counted as dropped, because the load generator never slows down for slow responses. A slow backend or a low limit is the usual reason.

**Fix.** Raise `load.max_concurrency`, or make the backend faster. A small number of drops during a spike is normal.

### The rates look lower than the target

**Cause.** `load.ramp_up` is set, so rates start near zero. The summary's ACTUAL column is an average over the whole run, so it reads lower than TARGET. The live view shows the last second, which is the value to compare.

**Fix.** Nothing, if the live view converges to TARGET. Otherwise check `dropped` and the upstream's latency.

### The console shows no colours

Colour is off when the output is not a terminal, and when `NO_COLOR` is set. Run the command directly in a terminal, and check that `NO_COLOR` is not set.

## Reporting a problem

When you report a problem, include the output of `kong-loadgen version`, the kongctl and decK versions, and the failing command, with the token removed. The tool never prints the token, but check your own terminal history before sharing it.
