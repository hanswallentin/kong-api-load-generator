# kong-loadgen

Configure Kong Konnect from one YAML file, then send demo traffic through the
gateway as simulated users and user groups. Konnect Analytics shows the traffic
broken down per consumer, so a presenter can say "this is mobile-app-03 from the
mobile-app group hitting the orders API".

Everything is driven by `config.yaml`. Edit it, run two commands, and watch the traffic.

## Quick start

Run these in order. Commands assume you are in this directory and have built the binary with `make build`.

**1. Install kongctl and decK.** Both are on PATH, and `configure` uses both.

```sh
brew install kong/kongctl/kongctl
brew install kong/deck/deck
```

Other platforms: see [kongctl](https://developer.konghq.com/kongctl/) and [decK](https://developer.konghq.com/deck/) for install instructions.
This tool was verified with kongctl 1.15.1 and decK 1.62.1. Newer releases exist, so check that the flags in `docs/usage.md` still match before a live demo.

**2. Create a Konnect personal access token.** Create it in Konnect, then copy the secret. [How to create and use a token](https://developer.konghq.com/kongctl/authentication/).
Export it in your shell. Never put it in `config.yaml`:

```sh
export KONNECT_TOKEN=<your personal access token>
export KONNECT_REGION=eu    # optional: overrides konnect.region in config.yaml
```

**3. Edit `config.yaml`.** At minimum, check:

- `konnect.region` and `konnect.control_plane`
- `gateway.proxy_url`, the data plane that traffic is sent to
- `upstream.default_url`. Use `http://host.docker.internal:9000` when Kong runs in Docker, and `http://localhost:9000` when Kong runs on the host.
- `endpoints`, `users` and `load`, for the traffic you want.

**4. Validate.** Problems are reported with line numbers.

```sh
./kong-loadgen validate
```

**5. Configure Konnect.** Preview first, then apply. The preview shows what will change.

```sh
./kong-loadgen configure --dry-run
./kong-loadgen configure
```

This creates the control plane (if `create_control_plane` is true), the services, routes, plugins, consumers and API keys, all tagged `kong-loadgen`. Running it again changes nothing.

**6. Start the upstream** (in a second terminal). Skip this if your upstream is a real service.

```sh
./kong-loadgen upstream
```

**7. Run the traffic.**

```sh
./kong-loadgen run
```

Open Konnect Analytics and group the Explorer by **Consumer**. Stop with Ctrl+C to print a summary. While `run` is going, save `config.yaml` to change rates, error percentages, identities, or add and remove endpoints. Changes apply within a few seconds.

**8. Clean up** when you are done. This removes only what this tool created:

```sh
./kong-loadgen teardown
```

## Notes

- The API keys in `config.yaml` (`mobile-01` and so on) are throwaway demo values. Never use real secrets in this file.
- The Konnect token is read from `KONNECT_TOKEN` and is never written to files or printed.
- The Analytics steps need traffic to reach a data plane that is attached to the control plane you configured. See [docs/troubleshooting.md](docs/troubleshooting.md).

## Documentation

- [docs/usage.md](docs/usage.md): every command and flag, with examples and sample output
- [docs/configuration.md](docs/configuration.md): every config key, the attribution buckets, and how to find consumers in Konnect
- [docs/demo-playbook.md](docs/demo-playbook.md): a 10-minute demo script
- [docs/troubleshooting.md](docs/troubleshooting.md): common problems and fixes
- [docs/architecture.md](docs/architecture.md): an overview for maintainers
