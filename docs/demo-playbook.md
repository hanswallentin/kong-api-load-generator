# Demo playbook: ten minutes with per-consumer analytics

This script walks an audience through Kong Konnect using live traffic from several user groups. It covers the baseline, a live error spike, a shift in traffic between groups, and rate limiting, and it shows the per-consumer breakdown in Konnect Analytics at each step.

Before you present, run through [Before the audience](#before-the-audience) once. It takes about ten minutes, and it catches most problems.

## Before the audience

1. Make sure a Kong data plane is running and attached to the control plane in `konnect.control_plane`. Traffic only reaches Konnect Analytics through a data plane that belongs to that control plane.
2. Set `upstream.default_url` to match where Kong runs (see [configuration](configuration.md#upstream)).
3. Validate, preview and apply:

   ```sh
   export KONNECT_TOKEN=<token>
   ./kong-loadgen validate
   ./kong-loadgen configure --dry-run
   ./kong-loadgen configure
   ```

4. Start the mock upstream in its own terminal, unless your upstream is a real service:

   ```sh
   ./kong-loadgen upstream
   ```

5. Open Konnect in two windows: the control plane's gateway configuration (services, routes, consumers), and Analytics, on the Explorer, grouped by **Consumer**. Set the time range to the last hour.
6. Do one dry run of the whole script with `./kong-loadgen run`, then stop it with Ctrl+C.

Keep the terminal running `run` on screen during the demo, and keep `config.yaml` open in an editor beside it. Saving the file applies changes.

## 0:00. Introduce the setup (1 minute)

Open `config.yaml` and point at three things:

- **Three endpoints.** `public-catalog` is anonymous and clean. `orders-api` is key-authenticated with 10% errors. `checkout-api` is key-authenticated and rate limited.
- **Two user groups.** `mobile-app` has 10 people and `partner-portal` has 3. Each person is a Kong consumer with an API key.
- **Throwaway keys.** Mention that the keys are demo values, and that the Konnect token comes from the environment, not the file.

Say it in one sentence: *the file describes the traffic, and the tool makes Konnect match it.*

## 1:00. Show the plan, then apply it (1 minute)

```sh
./kong-loadgen configure --dry-run
```

Point at the summary: a control plane, three services, three routes, the plugins, and one consumer per person. Then run `./kong-loadgen configure` without `--dry-run` if you have not already done it.

## 2:00. Show what Konnect now holds (2 minutes)

In the Konnect control plane:

- **Services and routes.** Show `orders-api` at `/orders` and `checkout-api` at `/checkout`. Show `public-catalog` at `/catalog` with no key-auth.
- **Plugins.** Open `orders-api`'s route, and show the key-auth plugin reading the `apikey` header.
- **A consumer.** Search for `mobile-app-03`. Show its tags (`kong-loadgen` and `mobile-app`) and its key-auth credential. Say: *this is mobile-app-03 from the mobile-app group.*

> If the labels differ in your release, the path is the same: control plane, consumers, search, open. See [configuration](configuration.md#finding-consumers-and-keys-in-konnect).

## 3:00. Start the baseline traffic (2 minutes)

In the terminal:

```sh
./kong-loadgen run
```

Narrate the live view as it refreshes. Point at:

- **TARGET and ACTUAL**, which should converge within the ramp-up period.
- **The BUCKET table.** `mobile-app` and `partner-portal` take about 70/30 of the orders traffic, plus their share of checkout. `Anonymous` holds the whole catalog endpoint.

Switch to Analytics, Explorer, grouped by Consumer. Show that the consumers with the most traffic are the mobile-app users, and that the catalog traffic appears under no consumer. Say: *anonymous requests have no consumer in Konnect, and the tool counts them the same way.*

## 5:00. Raise the error rate live (1.5 minutes)

Open `config.yaml`, find `orders-api`, and change:

```yaml
    error_percentage: 10   # change to 35
```

Save the file. Within a few seconds the terminal prints:

```text
config reloaded: 3 endpoint(s) updated, 0 added, 0 removed
```

Watch the orders-api ERROR column climb to about 35%. In Analytics, the status-code breakdown for `/orders` shows the new 4xx and 5xx mix. Say: *no restart, no redeploy. Change the file, and the traffic changes.*

Put it back to 10 before you move on, or keep it for the story and mention it later.

## 6:30. Shift traffic between groups (1 minute)

Change the weights:

```yaml
        - { group: mobile-app, weight: 70 }     # change to 30
        - { group: partner-portal, weight: 30 } # change to 70
```

Save. In the BUCKET table, `partner-portal` rises to about 70% of the orders traffic. In Analytics, the consumer breakdown changes to match. Say: *the share is set by the file, and the weights do not need to add up to 100.*

## 7:30. Add rate limiting (2 minutes)

`checkout-api` is already rate limited to 60 calls per minute per consumer. Show that the limit is in the file:

```yaml
    plugins:
      - name: rate-limiting
        config: { minute: 60 }
```

Change it to `minute: 20`, then save. The terminal warns:

```text
warning: changed since the last configure: plugins; run: kong-loadgen configure ...
```

Here the change is in the plugin, so the warning names the plugins. Run:

```sh
./kong-loadgen configure
```

The run keeps going while configure applies the change. Watch the 429 column on checkout-api climb, and show the 429s in Analytics. Say: *the warning tells you exactly what still needs pushing, and configure pushes only that.*

## 9:30. Show the attribution buckets (0.5 minutes)

Point at the BUCKET table, and at the `Invalid-credentials` row. Say: *the tool knows what it sent, so every request is counted once, and wrong keys are shown separately from anonymous traffic.*

## 10:00. Wrap up (0.5 minutes)

Stop the traffic with Ctrl+C. The run prints a summary. Then tidy up:

```sh
./kong-loadgen teardown
```

Teardown lists what it will remove and asks for confirmation. It removes only the objects tagged `kong-loadgen`, and deletes the control plane only if this tool created it.

## If something goes wrong on stage

| Symptom | Quick fix |
| --- | --- |
| The live view shows 5xx on every endpoint | The mock upstream is not running, or `upstream.default_url` does not match where Kong sends traffic. Start `./kong-loadgen upstream`, and check the URL. |
| Analytics is empty | Traffic is not reaching the data plane attached to this control plane. Check `gateway.proxy_url` and the data plane. |
| The terminal shows `configure has not been run` | Run `./kong-loadgen configure`. |
| Edits do not seem to apply | Check the terminal for `config reload rejected`. The message gives the line number. |
| Every request is 401 | The key header in `users.key_header` does not match the plugin. See [troubleshooting](troubleshooting.md#401-from-the-gateway). |

More detail is in [troubleshooting](troubleshooting.md).
