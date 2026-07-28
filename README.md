# scry

scry is a deliberately small status daemon for a hand-curated estate. It combines active TCP and HTTP probes with passive job check-ins, keeps transition state across restarts, renders one status page, and announces meaningful changes through Mattermost and SMTP.

Implementation follows the staged [work order](docs/future/scry-work-order.md). Stages 1 and 2 currently provide the repository skeleton, validated configuration, transition engine, and durable state file. Active scheduling and runtime listeners land in later human-gated stages. The as-built core is described in [docs/current/engine-and-state.md](docs/current/engine-and-state.md).

## Build

```sh
make test
make build
```

The binary installs to `$(go env GOPATH)/bin/scry`. Use `go build -tags no_ui ./cmd/scry` for a headless build.

## Configuration

Copy `scry.yaml.example` to `scry.yaml`, or pass an explicit file:

```sh
scry --config /path/to/scry.yaml
```

Configuration cascades from compiled defaults through `~/.config/scry/config.yaml`, `./scry.yaml`, and finally `--config`, with later sources taking precedence. Any malformed or invalid configuration stops boot.
